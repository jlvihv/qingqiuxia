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
	"strings"
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

	started atomic.Bool
	logger  *log.Logger

	// mu guards the fields a Start or a Stop changes, so that the proxy can be
	// moved to another port, or stopped and started again, while the process
	// runs.
	mu         sync.Mutex
	active     map[net.Conn]struct{}
	transports map[string]*http.Transport
	// stopDone is closed when the running server has finished, so a Stop can
	// wait for every connection to be done rather than cut them.
	stopDone chan struct{}
	// stopCtx is the parent of every request's context, and stopCancel ends
	// them. A request blocked far from its socket — on a breakpoint waiting for
	// a person, say — has nothing to unblock it when the port closes, so Stop
	// cancels the exchanges themselves rather than only their connections.
	stopCtx    context.Context
	stopCancel context.CancelFunc
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

// Start listens on addr and begins serving in the background. It may be called
// again on a stopped proxy to move it to another port, which is what a user
// changing the port while the proxy is off expects.
//
// addr may be ":0", or a bare port, for any free one.
func (p *Proxy) Start(addr string) (string, error) {
	p.mu.Lock()
	if p.stopDone != nil {
		p.mu.Unlock()
		return "", fmt.Errorf("proxy: already running on %s", p.addr)
	}
	if addr != "" && !strings.Contains(addr, ":") {
		// "8899" is a port, which every user means by it.
		addr = "127.0.0.1:" + addr
	}
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		p.mu.Unlock()
		return "", err
	}
	p.ln = ln
	p.addr = ln.Addr().String()
	// Every request's context descends from this one, so Stop can end an
	// exchange that is waiting somewhere a closed socket does not reach.
	runCtx, runCancel := context.WithCancel(context.Background())
	srv := &http.Server{
		Handler:           p,
		ReadHeaderTimeout: p.tune.HandshakeTime,
		IdleTimeout:       p.tune.IdleTimeout,
		ErrorLog:          log.New(io.Discard, "", 0),
		BaseContext:       func(net.Listener) context.Context { return runCtx },
	}
	p.srv = srv
	p.stopCtx, p.stopCancel = runCtx, runCancel
	done := make(chan struct{})
	p.stopDone = done
	p.started.Store(true)
	// Listeners and connections are per run, so a stopped proxy starts clean.
	p.active = make(map[net.Conn]struct{})
	p.transports = make(map[string]*http.Transport, 2)
	p.mu.Unlock()

	go func() {
		defer close(done)
		if err := srv.Serve(&trackingListener{Listener: ln, p: p}); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			p.logf("proxy stopped: %v", err)
		}
	}()
	// The address is read from the listener, not from p.addr: a Stop may have
	// cleared that field between the unlock above and here.
	return ln.Addr().String(), nil
}

// Stop stops serving and closes every connection. The proxy can be started
// again afterwards, on the same port or another.
func (p *Proxy) Stop() error {
	p.mu.Lock()
	done, srv, addr, cancel := p.stopDone, p.srv, p.addr, p.stopCancel
	if done == nil {
		p.mu.Unlock()
		return nil
	}
	p.stopDone = nil
	p.srv = nil
	p.ln = nil
	p.addr = ""
	p.stopCtx, p.stopCancel = nil, nil
	p.started.Store(false)
	// The connections are collected here and closed after the lock is
	// released: closing one runs trackedConn.Close, which takes this same lock
	// to remove itself from the set. Doing it under the lock deadlocks, and a
	// deadlocked stop holds the proxy's port for ever.
	conns := make([]net.Conn, 0, len(p.active))
	for c := range p.active {
		conns = append(conns, c)
	}
	transports := make([]*http.Transport, 0, len(p.transports))
	for _, t := range p.transports {
		transports = append(transports, t)
	}
	p.active = make(map[net.Conn]struct{})
	p.transports = make(map[string]*http.Transport, 2)
	p.mu.Unlock()

	// Connections are cut rather than waited for: a proxy holding a client's
	// keep-alive open would otherwise keep the port for as long as the client
	// cares, and stopping is a decision the user already made.
	for _, c := range conns {
		c.Close()
	}
	// An exchange blocked on a breakpoint is not waiting on its socket, so
	// closing the connection does not reach it: cancelling the run's context
	// does, and the handler returns instead of holding Shutdown for its full
	// timeout.
	if cancel != nil {
		cancel()
	}
	for _, t := range transports {
		t.CloseIdleConnections()
	}

	var err error
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err = srv.Shutdown(ctx)
	}
	// The serve goroutine closes done once Serve returns.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	p.logf("proxy stopped on %s", addr)
	return err
}

// Addr returns the address the proxy listens on, empty while stopped.
func (p *Proxy) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.addr
}

// SetLogger makes the proxy report connection failures to l, for a caller that
// wants to see them; a nil logger is silent.
func (p *Proxy) SetLogger(l *log.Logger) {
	p.mu.Lock()
	p.logger = l
	p.mu.Unlock()
}

// Replay sends a recorded request again. It goes through the policy like any
// other exchange, so a replay is captured, runs the rules and can be paused.
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
	if decision.Delay > 0 {
		t := time.NewTimer(decision.Delay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			p.policy.Complete(ex, nil, ctx.Err())
			return ctx.Err()
		}
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
	// A replay is not sent to a client, so the body it got belongs to nobody:
	// close it rather than leave the origin's connection open. A stream holds
	// the origin's socket, which would keep streaming for ever.
	defer resp.Close()
	if final, err := p.policy.Response(ctx, ex, resp); err == nil && final != nil {
		if final != resp {
			defer final.Close()
		}
		resp = final
	}
	ex.Response = resp
	p.policy.Complete(ex, resp, nil)
	return nil
}

// Running reports whether the proxy is serving.
func (p *Proxy) Running() bool { return p.started.Load() }

// Close stops the proxy for good.
func (p *Proxy) Close() error {
	return p.Stop()
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
	p.mu.Lock()
	l := p.logger
	p.mu.Unlock()
	if l != nil {
		l.Printf(format, args...)
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
	// An upgrade (WebSocket, and anything else carried on Connection: Upgrade)
	// stops being HTTP once the origin answers 101, so it cannot go through the
	// usual request/response capture: after the handshake the connection carries
	// a protocol this proxy does not parse. It is captured as a session and then
	// tunnelled byte for byte.
	if httpx.IsUpgrade(r.Header) {
		p.upgrade(w, r)
		return
	}
	p.forward(w, r)
}

// upgrade records the handshake and then tunnels the connection.
//
// A request that asks to upgrade stops being HTTP once the origin answers 101:
// after that the connection carries a protocol this proxy does not parse, so it
// cannot go through the ordinary capture-and-forward path. The handshake is
// recorded as a session — the request and the 101, which is what says the socket
// exists and where it goes — and every byte after it is passed through.
func (p *Proxy) upgrade(w http.ResponseWriter, r *http.Request) {
	ex := &Exchange{Started: time.Now()}
	req, overflow, err := captureRequest(r, p.tune.MaxBodyBytes)
	if err != nil {
		http.Error(w, "proxy: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer req.Close()
	ex.Request = req
	p.policy.Begin(ex)

	decision, err := p.policy.Request(r.Context(), ex)
	if err != nil {
		p.policy.Complete(ex, nil, err)
		http.Error(w, "proxy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if decision.Abort || decision.Mock != nil {
		resp := decision.Mock
		if resp == nil {
			resp = statusResponse(decision.Status)
		}
		p.finish(w, r, ex, resp, nil)
		return
	}

	client, br, err := hijack(w)
	if err != nil {
		p.policy.Complete(ex, nil, err)
		http.Error(w, "proxy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer client.Close()

	upstream, err := p.dialOrigin(r.Context(), req.URL)
	if err != nil {
		p.policy.Complete(ex, nil, err)
		io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer upstream.Close()

	if err := writeHandshake(upstream, req, overflow); err != nil {
		p.policy.Complete(ex, nil, err)
		io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}

	// The response head is read so the session can record it, then written back
	// as the origin wrote it. Whatever follows the head belongs to the tunnelled
	// protocol and is passed through untouched.
	upstreamReader := bufio.NewReader(upstream)
	raw, err := http.ReadResponse(upstreamReader, nil)
	if err != nil {
		p.policy.Complete(ex, nil, err)
		io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	resp := &msg.Response{
		Status:  raw.StatusCode,
		Reason:  raw.Status,
		Proto:   raw.Proto,
		Message: msg.Message{Header: raw.Header},
	}
	ex.Response = resp

	if raw.StatusCode != http.StatusSwitchingProtocols {
		// The handshake was refused and this is an ordinary response: capture it
		// and answer normally, so a rejected upgrade is recorded with its reason
		// like any other exchange.
		captured, _, cerr := captureResponse(raw, p.tune.MaxBodyBytes, time.Now())
		if cerr == nil && captured != nil {
			captured.Status, captured.Reason = resp.Status, resp.Reason
			resp = captured
			ex.Response = resp
		}
		p.policy.Complete(ex, resp, cerr)
		writeResponse(w, r, resp)
		return
	}

	if err := writeResponseHead(client, raw); err != nil {
		p.policy.Complete(ex, resp, err)
		return
	}
	p.policy.Complete(ex, resp, nil)

	// The connection is now the upgraded protocol, both ways. Bytes the client
	// sent after its handshake head, and bytes the origin buffered after its
	// 101, are part of it.
	var fromClient io.Reader = client
	if br != nil {
		fromClient = br
	}
	pipeBuffered(client, upstream, fromClient, upstreamReader)
}

// dialOrigin opens a connection to the origin named by u, with TLS when the URL
// is an HTTPS one. The origin's certificate is not verified, as a debugging
// proxy must not: see Tuning.UpstreamTLS.
func (p *Proxy) dialOrigin(ctx context.Context, u *url.URL) (net.Conn, error) {
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			host = net.JoinHostPort(u.Hostname(), "443")
		} else {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	conn, err := p.dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" {
		return conn, nil
	}
	cfg := p.tune.UpstreamTLS.Clone()
	if cfg.ServerName == "" {
		cfg.ServerName = u.Hostname()
	}
	tlsConn := tls.Client(conn, cfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

// writeHandshake writes a request to an origin in the form an origin expects:
// the path in the request line, the host in Host, and the connection headers
// kept, since an upgrade needs them to reach the origin.
func writeHandshake(w io.Writer, req *msg.Request, body io.Reader) error {
	target := "/"
	if req.URL != nil {
		target = req.URL.RequestURI()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", req.Method, target)
	fmt.Fprintf(&b, "Host: %s\r\n", req.URL.Host)
	for k, vv := range req.Header {
		switch {
		case strings.EqualFold(k, "Host"), strings.EqualFold(k, "Content-Length"):
			// Written above, or set from the body below.
			continue
		}
		for _, v := range vv {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	if len(req.Body) > 0 {
		fmt.Fprintf(&b, "Content-Length: %d\r\n", len(req.Body))
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	if len(req.Body) > 0 {
		if _, err := w.Write(req.Body); err != nil {
			return err
		}
	}
	if body != nil {
		if _, err := io.Copy(w, body); err != nil {
			return err
		}
	}
	return nil
}

// writeResponseHead forwards the origin's status line and headers as they were,
// so an upgrade response reaches the client unchanged.
func writeResponseHead(w io.Writer, raw *http.Response) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\r\n", raw.Proto, raw.Status)
	if err := raw.Header.Write(&b); err != nil {
		return err
	}
	b.WriteString("\r\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// pipeBuffered copies both directions until either side is done, starting from
// readers that may already hold bytes.
func pipeBuffered(client, upstream net.Conn, fromClient io.Reader, fromOrigin io.Reader) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, fromClient); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, fromOrigin); done <- struct{}{} }()
	<-done
	client.SetReadDeadline(time.Now().Add(time.Second))
	upstream.SetReadDeadline(time.Now().Add(time.Second))
	<-done
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
	// A captured body is read to the end, so its stream is done with and can be
	// closed; a stream is passed to the client, which closes it by closing the
	// response. resp.Stream is set exactly when there are unread bytes left, so
	// that is the test, not whether Body is empty.
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
	// Accept-Encoding is left as the client sent it. The transport has
	// compression off (see transportFor): it must not negotiate a coding of its
	// own and decode it, because the body the origin sent has to reach the
	// client exactly as the origin wrote it, and the capture decodes a copy for
	// display.
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
//
// A body longer than the capture limit arrives as both: Body is the start that
// was kept, and Stream is the rest that was never buffered. Both are written, so
// the client gets the response the origin sent rather than the part the capture
// happened to keep.
func writeResponse(w http.ResponseWriter, r *http.Request, resp *msg.Response) {
	h := w.Header()
	httpx.CopyHeader(h, resp.Header)
	httpx.RemoveHopHeaders(h)
	if resp.Stream != nil {
		// The length is unknown, and announcing one would make the client
		// wait for bytes that never come.
		h.Del("Content-Length")
	} else if resp.Header.Get("Content-Length") != "" {
		h.Set("Content-Length", strconv.Itoa(len(resp.Body)))
	}
	w.WriteHeader(resp.Status)
	if r.Method == http.MethodHead {
		return
	}
	if len(resp.Body) > 0 {
		if _, err := w.Write(resp.Body); err != nil {
			resp.Close()
			return
		}
	}
	if resp.Stream != nil {
		defer resp.Close()
		copyStream(w, resp.Stream)
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

// trackingListener remembers accepted connections, so that stopping the proxy
// can close them: a client's keep-alive would otherwise hold the port open, and
// stopping is a decision the user already made.
type trackingListener struct {
	net.Listener
	p *Proxy
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	p := l.p
	p.mu.Lock()
	// The proxy may have been stopped between the accept and here.
	if p.active == nil {
		p.mu.Unlock()
		c.Close()
		return nil, net.ErrClosed
	}
	p.active[c] = struct{}{}
	p.mu.Unlock()
	return &trackedConn{Conn: c, p: p}, nil
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
		p := c.p
		p.mu.Lock()
		delete(p.active, c.Conn)
		p.mu.Unlock()
	})
	return err
}
