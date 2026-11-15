// Package proxy is the core's MITM proxy: it accepts HTTP and HTTPS traffic,
// records it, runs it through the rules and breakpoints, and forwards it.
//
// The proxy knows nothing about how it is configured; it asks a Policy for a
// decision on each exchange. That keeps the network code apart from the rule
// engine, the session store and the control API, which can then change without
// touching the part that handles connections.
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/httpx"
	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// Tuning holds the numbers a proxy runs with. The zero value is usable: New
// fills in the defaults.
type Tuning struct {
	// MaxBodyBytes bounds how much of a body is kept in memory for inspection,
	// rules and the GUI. Larger bodies stream through untouched, with only
	// their first part recorded.
	MaxBodyBytes int64
	// DialTimeout, handshake timeouts and the like follow net/http's own
	// defaults when zero.
	DialTimeout   time.Duration
	IdleTimeout   time.Duration
	HandshakeTime time.Duration
	// UpstreamTLS configures how the proxy talks to origins. When nil the
	// proxy does not verify origin certificates, as a debugging proxy must
	// not: the origins a developer points it at are the ones with self-signed
	// certificates, expired ones, or names that only resolve on their machine.
	UpstreamTLS *tls.Config
}

// DefaultMaxBodyBytes is how much of a body a session keeps.
const DefaultMaxBodyBytes = 4 << 20 // 4 MiB

func (t *Tuning) fill() {
	if t.MaxBodyBytes <= 0 {
		t.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if t.DialTimeout <= 0 {
		t.DialTimeout = 15 * time.Second
	}
	if t.IdleTimeout <= 0 {
		t.IdleTimeout = 90 * time.Second
	}
	if t.HandshakeTime <= 0 {
		t.HandshakeTime = 10 * time.Second
	}
	if t.UpstreamTLS == nil {
		t.UpstreamTLS = &tls.Config{
			MinVersion: tls.VersionTLS12,
			// See UpstreamTLS: verification is the client's job, and the
			// proxy's job is to see traffic a client would otherwise hide.
			InsecureSkipVerify: true,
		}
	}
}

// Proxy is the running proxy server.
type Proxy struct {
	policy Policy
	tune   Tuning

	ln   net.Listener
	addr string
	srv  *http.Server

	// dialer is the origin dialer; it is replaceable in tests.
	dialer *net.Dialer

	started    atomic.Bool
	closed     atomic.Bool
	logger     *log.Logger
	mu         sync.Mutex
	active     map[net.Conn]struct{}
	transports map[string]*http.Transport
}

// New returns a proxy that asks p about every exchange. Call Serve or Listen to
// start it.
func New(p Policy, tune Tuning) *Proxy {
	tune.fill()
	return &Proxy{
		policy: p,
		tune:   tune,
		dialer: &net.Dialer{Timeout: tune.DialTimeout, KeepAlive: 30 * time.Second},
		active: make(map[net.Conn]struct{}),
	}
}

// Policy decides what happens to each exchange and records it. The proxy calls
// it from several goroutines at once, so an implementation must be safe for
// concurrent use.
type Policy interface {
	// Begin records a new request and returns the session it belongs to. The
	// proxy calls it once per exchange, before anything else.
	Begin(w *Exchange)
	// Request runs the request rules and breakpoints, and reports what to do
	// with the request.
	Request(ctx context.Context, ex *Exchange) (Decision, error)
	// Response runs the response rules and breakpoints on the response the
	// origin returned.
	Response(ctx context.Context, ex *Exchange, resp *msg.Response) (*msg.Response, error)
	// Complete records the end of the exchange.
	Complete(ex *Exchange, resp *msg.Response, err error)
	// Intercepts reports whether a TLS connection to this host is decrypted
	// rather than tunnelled.
	Intercepts(host string) bool
	// Certificate returns the certificate to present to a client that
	// connects to host.
	Certificate(host string) (*tls.Certificate, error)
}

// Decision is what the policy wants done with a request.
type Decision struct {
	// Mock, when set, is the response to send instead of contacting the origin.
	Mock *msg.Response
	// Abort refuses the request; Status is what the client gets.
	Abort  bool
	Status int
	// Delay postpones forwarding.
	Delay time.Duration
}

// Exchange is one request and its response as the proxy handles them.
type Exchange struct {
	// ID is the session ID the policy gave the exchange.
	ID uint64
	// Request is the request as the rules have left it.
	Request *msg.Request
	// Response is the response, once there is one.
	Response *msg.Response
	// Started is when the exchange began.
	Started time.Time

	mu    sync.Mutex
	times Timings

	// Policy is a slot the policy may use to keep its own state for an
	// exchange, so that a policy need not index every in-flight exchange in a
	// shared map: the proxy only carries the value from Begin to Complete.
	Policy any
}

// Timings returns where the exchange's time went.
func (ex *Exchange) Timings() Timings {
	ex.mu.Lock()
	defer ex.mu.Unlock()
	return ex.times
}

// setTimings records where the exchange's time went.
func (ex *Exchange) setTimings(t Timings) {
	ex.mu.Lock()
	ex.times = t
	ex.mu.Unlock()
}

// Listen starts the proxy on addr, which may be ":0" for any free port.
func (p *Proxy) Listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	p.ln = ln
	p.addr = ln.Addr().String()
	p.started.Store(true)
	return nil
}

// Replay sends a recorded request again. It goes through the policy like any
// other exchange, so a replay is captured, runs the rules and can be paused:
// that is what makes "send it again with one header changed" useful.
//
// The response is discarded; the session holds it.
func (p *Proxy) Replay(ctx context.Context, req *msg.Request) error {
	// Clone once: the replayed request is edited by rules, and the snapshot it
	// came from belongs to the session store.
	ex := &Exchange{Started: time.Now(), Request: req.Clone()}
	p.policy.Begin(ex)

	decision, err := p.policy.Request(ctx, ex)
	if err != nil {
		p.policy.Complete(ex, nil, err)
		return err
	}
	if decision.Abort || decision.Mock != nil {
		resp := decision.Mock
		if resp == nil {
			resp = statusResponse(decision.Status)
		}
		ex.Response = resp
		p.policy.Complete(ex, resp, nil)
		return nil
	}
	resp, times, err := p.roundTrip(ctx, ex.Request, nil)
	if err != nil {
		p.policy.Complete(ex, nil, err)
		return err
	}
	ex.setTimings(times)
	if final, err := p.policy.Response(ctx, ex, resp); err == nil && final != nil {
		resp = final
	}
	ex.Response = resp
	p.policy.Complete(ex, resp, nil)
	return nil
}

// SetLogger makes the proxy report connection failures to l, for a caller that
// wants to see them; a nil logger is silent.
func (p *Proxy) SetLogger(l *log.Logger) { p.logger = l }

// Addr returns the address the proxy listens on.
func (p *Proxy) Addr() string { return p.addr }

// Serve accepts connections until Close. Listen must have been called, or addr
// is empty and the proxy listens on 127.0.0.1:8899.
func (p *Proxy) Serve() error {
	if p.ln == nil {
		if err := p.Listen("127.0.0.1:8899"); err != nil {
			return err
		}
	}
	srv := &http.Server{
		Handler:           p,
		ReadHeaderTimeout: p.tune.HandshakeTime,
		IdleTimeout:       p.tune.IdleTimeout,
		ErrorLog:          log.New(io.Discard, "", 0),
		// A debugging proxy must not try to be clever about errors the client
		// should see.
		DisableGeneralOptionsHandler: false,
	}
	p.srv = srv
	err := srv.Serve(&trackingListener{Listener: p.ln, p: p})
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// trackingListener remembers accepted connections so that Close can close them
// and Serve can wait.
type trackingListener struct {
	net.Listener
	p *Proxy
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.p.mu.Lock()
	l.p.active[c] = struct{}{}
	l.p.mu.Unlock()
	return &trackedConn{Conn: c, p: l.p}, nil
}

// trackedConn removes itself from the proxy's set when it closes.
type trackedConn struct {
	net.Conn
	p    *Proxy
	once sync.Once
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.p.mu.Lock()
		delete(c.p.active, c.Conn)
		c.p.mu.Unlock()
	})
	return err
}

// Close stops the proxy, closing its listener and every connection it serves.
func (p *Proxy) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	var err error
	if p.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err = p.srv.Shutdown(ctx)
	}
	p.mu.Lock()
	for c := range p.active {
		c.Close()
	}
	p.mu.Unlock()
	return err
}

// ServeHTTP handles every connection the proxy accepts: a plain HTTP request,
// or a CONNECT that opens a tunnel, which is decrypted when the host is
// intercepted and passed through otherwise.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// handleConnect answers a CONNECT, which every HTTPS client sends to a proxy.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	defer func() {
		// A panic in one connection must not take the whole proxy down: the
		// client sees its connection close, the tool keeps serving.
		if v := recover(); v != nil {
			p.logf("panic serving CONNECT %s: %v", r.Host, v)
		}
	}()
	if !p.policy.Intercepts(r.Host) {
		p.tunnel(w, r)
		return
	}
	if err := p.intercept(w, r); err != nil {
		// The response may already be committed by the hijack; nothing to do
		// but end the connection.
		p.logf("intercept %s: %v", r.Host, err)
	}
}

// logf writes a diagnostic line when the proxy runs with a logger.
func (p *Proxy) logf(format string, args ...any) {
	if p.logger != nil {
		p.logger.Printf(format, args...)
	}
}

// hijack takes the client connection from the ResponseWriter.
func hijack(w http.ResponseWriter) (net.Conn, *bufio.Reader, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("proxy: the connection cannot be hijacked")
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	return conn, brw.Reader, nil
}

// writeConnectOK writes the 200 a CONNECT is answered with to open a tunnel.
func writeConnectOK(conn net.Conn) error {
	_, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	return err
}

// tunnel passes a CONNECT through without decrypting it.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	client, br, err := hijack(w)
	if err != nil {
		http.Error(w, "proxy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	upstream, err := p.dialer.Dial("tcp", r.Host)
	if err != nil {
		io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer upstream.Close()
	if err := writeConnectOK(client); err != nil {
		return
	}
	// Bytes the client sent after the CONNECT are part of the tunnel.
	if br != nil && br.Buffered() > 0 {
		buffered, _ := br.Peek(br.Buffered())
		if _, err := upstream.Write(buffered); err != nil {
			return
		}
	}
	pipe(client, upstream)
}

// pipe copies both ways until either side is done.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); done <- struct{}{} }()
	<-done
	// Let the slower direction finish when the other one has closed.
	a.SetReadDeadline(time.Now().Add(time.Second))
	b.SetReadDeadline(time.Now().Add(time.Second))
	<-done
}

// intercept terminates TLS for a CONNECT and serves the decrypted requests.
func (p *Proxy) intercept(w http.ResponseWriter, r *http.Request) error {
	client, br, err := hijack(w)
	if err != nil {
		http.Error(w, "proxy: "+err.Error(), http.StatusInternalServerError)
		return err
	}
	defer client.Close()

	cert, err := p.policy.Certificate(r.Host)
	if err != nil {
		io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return err
	}
	if err := writeConnectOK(client); err != nil {
		return err
	}

	// The TLS handshake starts after the 200, with any bytes the client
	// already sent.
	var raw net.Conn = client
	if br != nil && br.Buffered() > 0 {
		raw = &bufferedConn{Conn: client, r: br}
	}
	tlsConn := tls.Server(raw, p.clientTLSConfig(cert))
	defer tlsConn.Close()
	if err := tlsConn.SetDeadline(time.Now().Add(p.tune.HandshakeTime)); err != nil {
		return err
	}
	if err := tlsConn.Handshake(); err != nil {
		return err
	}
	// The handshake's deadline must not outlive the handshake.
	if err := tlsConn.SetDeadline(time.Time{}); err != nil {
		return err
	}

	return p.serveConn(tlsConn, r.Host)
}

// bufferedConn serves bytes already buffered before reading from the
// connection, as a client's early TLS records are.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// clientTLSConfig is the TLS configuration the proxy presents to the client.
//
// Only HTTP/1.1 is offered: the tunnel is served by one connection's worth of
// http.Server, which does not speak HTTP/2. Advertising h2 here would let a
// client negotiate a protocol the proxy then cannot answer.
func (p *Proxy) clientTLSConfig(cert *tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{*cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"http/1.1"},
	}
}

// serveConn reads HTTP requests off an established connection until the client
// stops sending.
//
// The server is given a listener that yields this one connection and then
// blocks, so that http.Server's accept loop does not decide the connection is
// over before it has been served. Keep-alive works as usual inside the tunnel.
func (p *Proxy) serveConn(conn net.Conn, host string) error {
	done := make(chan struct{})
	var once sync.Once
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A request inside the tunnel has only a path: the CONNECT named
			// the origin, and the tunnel decrypted to it.
			if !r.URL.IsAbs() {
				r.URL.Scheme = "https"
				r.URL.Host = r.Host
			}
			p.handleHTTP(w, r)
		}),
		ReadHeaderTimeout: p.tune.HandshakeTime,
		IdleTimeout:       p.tune.IdleTimeout,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	l := &singleConnListener{
		conn: &doneConn{Conn: conn, onClose: func() { once.Do(func() { close(done) }) }},
		addr: conn.LocalAddr(),
		done: done,
	}
	err := srv.Serve(l)
	if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// singleConnListener yields one connection, then blocks until that connection
// is closed and reports the listener closed.
type singleConnListener struct {
	conn net.Conn
	addr net.Addr
	done chan struct{}
	once sync.Once
	// taken reports that the connection has been handed out.
	taken bool
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	if !l.taken {
		l.taken = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error   { return nil }
func (l *singleConnListener) Addr() net.Addr { return l.addr }

// doneConn reports when the server is finished with a connection.
type doneConn struct {
	net.Conn
	once    sync.Once
	onClose func()
}

func (c *doneConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.onClose)
	return err
}

// handleHTTP serves one request, decrypted or plain, through the policy.
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	// A proxied HTTP request carries its absolute URL in the request line.
	if !r.URL.IsAbs() {
		// A request that came through a decrypted tunnel has only a path; the
		// Host header names the origin and the scheme is https.
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		r.URL.Scheme = scheme
		r.URL.Host = r.Host
	}
	p.forward(w, r)
}

// forward runs one exchange: capture, policy, origin, policy, response.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ex := &Exchange{Started: time.Now()}

	// Read the request body, up to the inspection limit. A body that goes past
	// it is forwarded in full: the session keeps the start, the client's bytes
	// keep flowing.
	req, overflow, err := captureRequest(r, p.tune.MaxBodyBytes)
	if err != nil {
		http.Error(w, "proxy: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer req.Close()
	ex.Request = req
	p.policy.Begin(ex)

	decision, err := p.policy.Request(ctx, ex)
	if err != nil {
		p.policy.Complete(ex, nil, err)
		http.Error(w, "proxy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if decision.Delay > 0 {
		timer := time.NewTimer(decision.Delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			p.policy.Complete(ex, nil, ctx.Err())
			return
		}
	}
	// A rule may answer the request itself, which ends the exchange here.
	if decision.Abort || decision.Mock != nil {
		resp := decision.Mock
		if resp == nil {
			resp = statusResponse(decision.Status)
		}
		p.finish(w, r, ex, resp, nil)
		return
	}

	resp, times, err := p.roundTrip(ctx, req, overflow)
	if err != nil {
		p.policy.Complete(ex, nil, err)
		writeError(w, err)
		return
	}
	ex.setTimings(times)
	final, err := p.policy.Response(ctx, ex, resp)
	if err == nil && final != nil {
		resp = final
	}
	// A failing response rule still delivers the response: the client asked
	// for it, and a rule is the debugger's business, not the client's.
	p.finish(w, r, ex, resp, err)
}

// finish records the end of an exchange and sends the response, so that every
// way an exchange can end takes the same path.
func (p *Proxy) finish(w http.ResponseWriter, r *http.Request, ex *Exchange, resp *msg.Response, err error) {
	ex.Response = resp
	p.policy.Complete(ex, resp, err)
	writeResponse(w, r, resp)
}

// statusResponse is the empty response an abort answers with.
func statusResponse(status int) *msg.Response {
	if status == 0 {
		status = http.StatusForbidden
	}
	return &msg.Response{
		Status:  status,
		Reason:  http.StatusText(status),
		Message: msg.Message{Header: http.Header{}},
	}
}

// roundTrip sends the request to the origin and captures the response.
func (p *Proxy) roundTrip(ctx context.Context, req *msg.Request, overflow io.Reader) (*msg.Response, Timings, error) {
	outReq, err := p.buildUpstreamRequest(ctx, req, overflow)
	if err != nil {
		return nil, Timings{}, err
	}

	transport := p.transportFor(req.URL.Scheme)
	trace := &httptraceTimes{}
	outReq = outReq.WithContext(httptraceWithTimes(ctx, trace))

	start := time.Now()
	raw, err := transport.RoundTrip(outReq)
	if err != nil {
		return nil, Timings{}, unwrapURLError(err)
	}

	resp, ttfb, err := captureResponse(raw, p.tune.MaxBodyBytes, start)
	// A captured body is read to the end, so its stream can be closed; a
	// stream is passed to the client, which closes it by closing the response.
	if resp == nil || resp.Stream == nil {
		defer raw.Body.Close()
	}
	if err != nil {
		return nil, Timings{}, err
	}
	return resp, trace.collect(ttfb), nil
}

// buildUpstreamRequest turns a captured request into the one to send.
func (p *Proxy) buildUpstreamRequest(ctx context.Context, req *msg.Request, overflow io.Reader) (*http.Request, error) {
	u := *req.URL
	body := io.Reader(nil)
	if len(req.Body) > 0 {
		// bytes.NewReader, not strings.NewReader(string(b)): the latter copies
		// the whole body to make it a string, for no gain.
		body = bytes.NewReader(req.Body)
	}
	// An overflowing body continues from where the capture stopped; the two
	// readers are joined so the origin sees the whole body.
	if overflow != nil {
		if body == nil {
			body = overflow
		} else {
			body = io.MultiReader(body, overflow)
		}
	}
	outReq, err := http.NewRequestWithContext(ctx, req.Method, u.String(), body)
	if err != nil {
		return nil, err
	}
	// The upstream request gets its own header map: the captured one belongs to
	// the session, which a GUI may be reading, and hop-by-hop headers have to
	// be dropped from this copy alone.
	outReq.Header = httpx.CloneWithoutHopHeaders(req.Header)
	// The proxy speaks its own connection's negotiation to the origin.
	outReq.Header.Del("Accept-Encoding")
	if overflow != nil {
		// The length is unknown once the body streams from two readers.
		outReq.ContentLength = -1
		outReq.Header.Del("Content-Length")
	} else if len(req.Body) > 0 || req.Header.Get("Content-Length") != "" {
		outReq.ContentLength = int64(len(req.Body))
	}
	outReq.Host = req.URL.Host
	return outReq, nil
}

// transportFor returns the transport for a scheme, created once.
func (p *Proxy) transportFor(scheme string) *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.transports == nil {
		p.transports = make(map[string]*http.Transport, 2)
	}
	if t, ok := p.transports[scheme]; ok {
		return t
	}
	t := &http.Transport{
		Proxy:               nil, // never proxy the proxy's own traffic
		DialContext:         p.dialer.DialContext,
		TLSClientConfig:     p.tune.UpstreamTLS.Clone(),
		TLSHandshakeTimeout: p.tune.HandshakeTime,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     p.tune.IdleTimeout,
		ForceAttemptHTTP2:   true,
		// The proxy forwards what the origin sent; a client that asked for
		// compression gets it, and the capture decodes for display.
		DisableCompression: true,
		// A response that a rule may rewrite must arrive decompressed only
		// when the rule asked for it, so keep the wire form.
	}
	if scheme == "https" {
		t.ForceAttemptHTTP2 = true
	}
	p.transports[scheme] = t
	return t
}

// writeResponse sends a captured response to the client.
//
// A buffered body goes out in one write; a stream is copied as it arrives and
// flushed at every chunk, since net/http would otherwise hold the response
// headers until the stream ended, which for server-sent events is never.
func writeResponse(w http.ResponseWriter, r *http.Request, resp *msg.Response) {
	h := w.Header()
	httpx.CopyHeader(h, resp.Header)
	httpx.RemoveHopHeaders(h)
	if resp.Stream == nil && resp.Header.Get("Content-Length") != "" {
		h.Set("Content-Length", strconv.Itoa(len(resp.Body)))
	}
	if resp.Stream != nil {
		// The length is unknown, and announcing one would make the client
		// wait for bytes that never come.
		h.Del("Content-Length")
	}
	w.WriteHeader(resp.Status)
	if r.Method == http.MethodHead {
		return
	}
	if resp.Stream != nil {
		copyStream(w, resp.Stream)
		return
	}
	if len(resp.Body) > 0 {
		_, _ = w.Write(resp.Body)
	}
}

// streamChunk is how much a stream copies between flushes: small enough that a
// client sees an event as soon as it arrives, large enough not to syscall per
// byte.
const streamChunk = 4 << 10

// copyStream forwards a live body, flushing what it read so the client sees
// each chunk as it comes.
func copyStream(w http.ResponseWriter, src io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, streamChunk)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// writeError answers a client whose request the origin could not serve.
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "qingqiuxia: %v\n", err)
}

// unwrapURLError digs the underlying error out of a *url.Error, whose message
// repeats the URL the session already shows.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
