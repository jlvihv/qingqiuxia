// Package api is the core's control plane: an HTTP server a GUI, a script or a
// browser talks to while the proxy runs.
//
// The API is the only supported way to observe and steer a running core, which
// keeps the GUI replaceable: it never links the proxy, it asks the API. The
// routes are plain JSON over HTTP, plus server-sent events for live traffic.
package api

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/pipeline"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// Core is what the API steers. The proxy's own state (listen address, system
// proxy settings) lives here too, so the API is the single place a GUI needs.
type Core struct {
	Pipeline *pipeline.Pipeline
	// Addr is where the proxy listens, for the GUI to show.
	Addr func() string
	// SetCapture turns capture on or off.
	SetCapture func(bool)
	// CaptureOn reports whether capture is on.
	CaptureOn func() bool
	// Version is the core's version, for the status call.
	Version string
	// Started is when the core started.
	Started time.Time
	// Repeat re-sends a recorded request, as a capture tool's replay does.
	Repeat func(s session.Snapshot, count int, delay, interval time.Duration)
	// Resources reports the process's resident memory in megabytes and its
	// goroutine count, for the window's status bar.
	Resources func() (memoryMB uint64, goroutines int)
	// Base is how a caller reaches this API, as "http://127.0.0.1:8898". The
	// index uses it so its examples can be pasted as they are.
	Base string
	// Proxy steers the proxy's own listener. It is what the control API offers
	// on /api/proxy, and it is separate from the traffic side: the proxy's
	// address is what a client is pointed at, while this API is what a tool
	// talks to, and the two live and die independently.
	Proxy ProxyController
	// Logger receives the API's request failures; nil is silent.
	Logger *log.Logger
}

// Server is the control API.
type Server struct {
	core Core
	// base is how a caller reaches this API, as "http://127.0.0.1:8898": the
	// index's examples use it so they can be pasted as they are.
	base  string
	mux   *http.ServeMux
	token string
	srv   *http.Server
	ln    net.Listener
}

// Options configures the API server.
type Options struct {
	// Token, when set, must be presented as a bearer token or a token query
	// parameter. The API listens on the loopback address by default, so a
	// token is only needed when it is exposed further.
	Token string
}

// New returns an API server for core.
func New(core Core, opts Options) *Server {
	s := &Server{core: core, mux: http.NewServeMux(), token: opts.Token}
	s.routes()
	return s
}

// routes wires the API's paths.
func (s *Server) routes() {
	// Status and control.
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("POST /api/capture", s.handleCapture)

	// Traffic.
	s.mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	s.mux.HandleFunc("GET /api/sessions/{id}", s.handleGetSession)
	s.mux.HandleFunc("DELETE /api/sessions", s.handleClearSessions)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.HandleFunc("POST /api/sessions/{id}/repeat", s.handleRepeat)

	// Rules.
	s.mux.HandleFunc("GET /api/rules", s.handleListRules)
	s.mux.HandleFunc("PUT /api/rules", s.handleReplaceRules)
	s.mux.HandleFunc("POST /api/rules", s.handleAddRule)
	s.mux.HandleFunc("PATCH /api/rules/{id}", s.handlePatchRule)
	s.mux.HandleFunc("DELETE /api/rules/{id}", s.handleDeleteRule)

	// Breakpoints.
	s.mux.HandleFunc("GET /api/breakpoints", s.handleListBreakpoints)
	s.mux.HandleFunc("PUT /api/breakpoints", s.handleReplaceBreakpoints)
	s.mux.HandleFunc("GET /api/pauses", s.handleListPauses)
	s.mux.HandleFunc("POST /api/pauses/{id}/resume", s.handleResume)
	s.mux.HandleFunc("POST /api/pauses/{id}/abort", s.handleAbortPause)
	s.mux.HandleFunc("PUT /api/pauses/{id}", s.handleEditPause)

	// Certificate.
	s.mux.HandleFunc("GET /api/certificate", s.handleCertificate)

	// The proxy's own listener, which is not this one.
	s.proxyRoutes()

	// What an agent reads first: how to use this API, in the shape that costs
	// it the fewest tokens.
	s.mux.HandleFunc("GET /api", s.handleIndex)
	s.mux.HandleFunc("GET /api/help", s.handleIndex)
}

// ServeHTTP applies the token check and then the routes.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.authorized(r) {
		writeError(w, http.StatusUnauthorized, "a token is required")
		return
	}
	s.mux.ServeHTTP(w, r)
}

// authorized checks the request's token.
func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// Listen starts the API on addr, which may be ":0" for any free port.
func (s *Server) Listen(addr string) error {
	network := "tcp"
	// A path, not an address: the window starts the core in its own process
	// and reaches its API over a unix socket, which no other process on the
	// machine can connect to and which needs no port or token. It is also
	// gone as soon as the socket file is unlinked.
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, ".") {
		network = "unix"
		// A socket left by a process that died would make Listen fail.
		os.Remove(addr)
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.srv = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		// A live event stream is a long request; the write timeout must not
		// cut it, and the read timeout is the caller's business.
		IdleTimeout: 120 * time.Second,
	}
	if s.core.Logger != nil {
		s.srv.ErrorLog = s.core.Logger
	}
	return nil
}

// Addr returns the address the API listens on.
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Serve serves the API until Close. Listen must have been called.
func (s *Server) Serve() error {
	if s.srv == nil {
		return errors.New("api: Listen must be called first")
	}
	err := s.srv.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Close stops the API.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Close()
}

// status is the payload of GET /api/status.
type status struct {
	Version string `json:"version"`
	// ProxyAddr is where a client points its proxy settings.
	ProxyAddr string `json:"proxyAddr"`
	// APIVersion is the API's own version, so a GUI can refuse a core it does
	// not understand.
	APIVersion int    `json:"apiVersion"`
	Uptime     string `json:"uptime"`
	Capturing  bool   `json:"capturing"`
	Sessions   int    `json:"sessions"`
	Rules      int    `json:"rules"`
	Breakpoint bool   `json:"breakpointEnabled"`
	Paused     int    `json:"paused"`
	Certs      int    `json:"cachedCertificates"`
	Started    string `json:"started"`
	// MemoryMB and Goroutines describe the process, which the window shows in
	// its status bar: a user looking at a permanent daemon wants to know what
	// it costs.
	MemoryMB   uint64 `json:"memoryMb"`
	Goroutines int    `json:"goroutines"`
}

// APIVersion is the version of the JSON API, bumped when a field changes
// meaning.
const APIVersion = 1

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := status{
		Version:    s.core.Version,
		APIVersion: APIVersion,
		Started:    s.core.Started.Format(time.RFC3339),
		Uptime:     time.Since(s.core.Started).Round(time.Second).String(),
	}
	if s.core.Addr != nil {
		st.ProxyAddr = s.core.Addr()
	}
	if s.core.CaptureOn != nil {
		st.Capturing = s.core.CaptureOn()
	}
	if s.core.Resources != nil {
		st.MemoryMB, st.Goroutines = s.core.Resources()
	}
	if p := s.core.Pipeline; p != nil {
		st.Sessions = p.Sessions().Len()
		st.Rules = p.Rules().Len()
		if m := p.Breakpoints(); m != nil {
			st.Breakpoint = m.Enabled()
			st.Paused = m.PauseCount()
		}
	}
	writeJSON(w, http.StatusOK, st)
}

// sessionJSON is how the API shows a session.
type sessionJSON struct {
	ID     uint64 `json:"id"`
	Phase  string `json:"phase"`
	Source string `json:"source"`
	Method string `json:"method"`
	URL    string `json:"url"`
	Host   string `json:"host"`
	Proto  string `json:"proto"`
	Status int    `json:"status"`
	// Request and Response carry the detail a viewer needs, and are filled for
	// a single session only: a list is rows, not bodies.
	Request  *messageJSON `json:"request,omitempty"`
	Response *messageJSON `json:"response,omitempty"`
	Error    string       `json:"error,omitempty"`

	// Timing in milliseconds, as a traffic list shows them.
	DurationMS float64 `json:"durationMs"`
	TTFBMS     float64 `json:"ttfbMs,omitempty"`
	DNSMS      float64 `json:"dnsMs,omitempty"`
	ConnectMS  float64 `json:"connectMs,omitempty"`
	TLSMS      float64 `json:"tlsMs,omitempty"`

	BytesIn   int64    `json:"bytesIn"`
	BytesOut  int64    `json:"bytesOut"`
	StartedAt string   `json:"startedAt"`
	Notes     []noteJS `json:"notes,omitempty"`
}

type messageJSON struct {
	Headers map[string][]string `json:"headers,omitempty"`
	// Body is the body as text when it is text, and base64 otherwise.
	Body string `json:"body,omitempty"`
	// BodyEncoding is "text" or "base64".
	BodyEncoding string `json:"bodyEncoding,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	// Streaming reports a body that is not buffered.
	Streaming bool `json:"streaming,omitempty"`
}

type noteJS struct {
	At   string `json:"at"`
	Rule string `json:"rule,omitempty"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// toSessionJSON turns a snapshot into the API's form.
func toSessionJSON(s session.Snapshot, detail bool) sessionJSON {
	out := sessionJSON{
		ID:         s.ID,
		Phase:      s.Phase.String(),
		Source:     s.Source.String(),
		DurationMS: ms(s.Timings.Duration()),
		TTFBMS:     ms(s.Timings.TTFB),
		DNSMS:      ms(s.Timings.DNS),
		ConnectMS:  ms(s.Timings.Connect),
		TLSMS:      ms(s.Timings.TLS),
		BytesIn:    s.BytesIn,
		BytesOut:   s.BytesOut,
		StartedAt:  s.Timings.Start.Format(time.RFC3339Nano),
		Error:      s.Err,
	}
	if r := s.Request; r != nil {
		out.Method = r.Method
		out.Proto = r.Proto
		if r.URL != nil {
			out.URL = r.URL.String()
			out.Host = r.URL.Host
		}
		if detail {
			m := toMessageJSON(&r.Message)
			out.Request = &m
		}
	}
	if r := s.Response; r != nil {
		out.Status = r.Status
		if detail {
			m := toMessageJSON(&r.Message)
			out.Response = &m
		}
	}
	for _, n := range s.Notes {
		out.Notes = append(out.Notes, noteJS{
			At: n.At.Format(time.RFC3339Nano), Rule: n.Rule, Kind: n.Kind, Text: n.Text,
		})
	}
	return out
}

// toMessageJSON describes a message's headers and body.
func toMessageJSON(m *msg.Message) messageJSON {
	out := messageJSON{
		Headers:     map[string][]string(m.Header),
		ContentType: m.Header.Get("Content-Type"),
		Truncated:   m.Truncated,
		Streaming:   m.Stream != nil,
	}
	if len(m.Body) > 0 {
		if msg.IsText(m.ContentType()) {
			out.Body = string(m.Body)
			out.BodyEncoding = "text"
		} else {
			out.Body = base64.StdEncoding.EncodeToString(m.Body)
			out.BodyEncoding = "base64"
		}
	}
	return out
}

func ms(d time.Duration) float64 {
	if d == 0 {
		return 0
	}
	return float64(d.Microseconds()) / 1000
}

// handleListSessions returns the traffic list, newest last.
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	f, err := parseListFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	store := s.core.Pipeline.Sessions()
	// 过滤在服务端做：按字节付费的调用方要的是它想要的那三行，不是两百行让它
	// 自己去筛。
	all := store.List(0, -1)
	kept := make([]session.Snapshot, 0, len(all))
	// 最新的在前，这也是流量列表的顺序。
	for i := len(all) - 1; i >= 0; i-- {
		if f.matches(&all[i]) {
			kept = append(kept, all[i])
		}
	}
	total := len(kept)
	end := min(f.offset+f.limit, total)
	if f.offset > total {
		f.offset = total
	}
	kept = kept[f.offset:end]

	if f.table {
		writeTable(w, f, kept, total, store.Dropped())
		return
	}
	out := make([]sessionJSON, 0, len(kept))
	for i := range kept {
		out = append(out, toSessionJSON(kept[i], false))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": out,
		"total":    total,
		"held":     store.Len(),
		"dropped":  store.Dropped(),
	})
}

// tableColumns 是 shape=table 能带的列，也是调用方不指定时的默认顺序。
var tableColumns = []string{
	"id", "method", "status", "ms", "host", "url", "size", "source", "phase",
}

// writeTable 用一个表头加数组行来回答。
//
// 每行一个对象意味着每行都重复一遍键名；两百行 {"id":1,"method":"GET",...}
// 比同样的数据用数组写要多约 40% 字节，而读的人是按上下文付费的。
func writeTable(w http.ResponseWriter, f listFilter, list []session.Snapshot, total int, dropped uint64) {
	cols := f.fields
	if len(cols) == 0 {
		cols = tableColumns
	}
	rows := make([][]any, 0, len(list))
	for i := range list {
		rows = append(rows, tableRow(&list[i], cols))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cols":    cols,
		"rows":    rows,
		"total":   total,
		"dropped": dropped,
	})
}

// tableRow 把一条会话渲染成指定列的值。
func tableRow(s *session.Snapshot, cols []string) []any {
	row := make([]any, len(cols))
	for i, c := range cols {
		switch c {
		case "id":
			row[i] = s.ID
		case "method":
			row[i] = methodOfSession(s)
		case "status":
			row[i] = statusOf(s)
		case "ms":
			row[i] = round1(s.Timings.Duration().Seconds() * 1000)
		case "host":
			row[i] = hostOf(s)
		case "url":
			row[i] = urlOf(s)
		case "size":
			row[i] = s.BytesOut
		case "source":
			row[i] = s.Source.String()
		case "phase":
			row[i] = s.Phase.String()
		default:
			row[i] = nil
		}
	}
	return row
}

// statusOf 是响应的状态码，还没有响应时是 0。
func statusOf(s *session.Snapshot) int {
	if s.Response != nil {
		return s.Response.Status
	}
	return 0
}

// round1 保留一位小数，毫秒数这样够了。
func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sessionID(w, r)
	if !ok {
		return
	}
	// 调用方通常只要一次请求的一部分："为什么 500"要的是摘要和响应体，请求头
	// 是噪音。分块的意义就在于，问耗时的人不会收到半个兆的 body。
	//
	// 先查 part 再查会话：参数写错就是参数写错，跟那个 id 存不存在无关；为它
	// 回 404 会让人去查错地方。
	q := r.URL.Query()
	part := q.Get("part")
	if part == "" {
		part = "all"
	}
	switch part {
	case "all", "body", "summary", "headers":
	default:
		writeError(w, http.StatusBadRequest,
			"part must be all, summary, headers or body")
		return
	}
	snap, found := s.core.Pipeline.Sessions().Get(id)
	if !found {
		writeError(w, http.StatusNotFound, "no session with that id")
		return
	}
	maxBody := intParam(r, "max", 0)
	out := toSessionJSON(snap, false)
	switch part {
	case "all", "body":
		out = toSessionJSON(snap, true)
	case "summary":
		// The row, which is what a list already gives, plus what the row
		// leaves out and a summary does not.
	case "headers":
		out = toSessionJSON(snap, false)
		if snap.Request != nil {
			out.Request = headersOnly(&snap.Request.Message, maxBody)
		}
		if snap.Response != nil {
			out.Response = headersOnly(&snap.Response.Message, maxBody)
		}
	}
	if maxBody > 0 {
		truncateBodies(out.Request, maxBody)
		truncateBodies(out.Response, maxBody)
	}
	writeJSON(w, http.StatusOK, out)
}

// headersOnly 只描述头部，不带 body。
func headersOnly(m *msg.Message, _ int) *messageJSON {
	if m == nil {
		return nil
	}
	return &messageJSON{
		Headers:     map[string][]string(m.Header),
		ContentType: m.Header.Get("Content-Type"),
		Truncated:   m.Truncated,
		Streaming:   m.Stream != nil,
	}
}

// truncateBodies 把 body 截到 n 字节，并且标明截断了：悄悄少一截的 body 会
// 让人以为工具坏了。
func truncateBodies(m *messageJSON, n int) {
	if m == nil || len(m.Body) <= n {
		return
	}
	// body 可能已经是 base64，按字节切会切断编码。对读的人来说切文本就够了，
	// 真实大小本来也在行里。
	m.Body = m.Body[:n]
	m.Truncated = true
}

func (s *Server) handleClearSessions(w http.ResponseWriter, r *http.Request) {
	n := s.core.Pipeline.Sessions().Clear()
	writeJSON(w, http.StatusOK, map[string]int{"cleared": n})
}

func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	if s.core.SetCapture != nil {
		s.core.SetCapture(*body.Enabled)
	}
	on := false
	if s.core.CaptureOn != nil {
		on = s.core.CaptureOn()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"capturing": on})
}

// sessionID parses the {id} path segment.
func (s *Server) sessionID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the id must be a number")
		return 0, false
	}
	return id, true
}

// writeJSON answers with v.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeError answers with a JSON error.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// decode reads a JSON request body into v, answering the client on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	defer io.Copy(io.Discard, r.Body)
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// intParam reads an integer query parameter.
func intParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// ruleID parses the {id} path segment of a rule.
func ruleID(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the id must be a number")
		return 0, false
	}
	return id, true
}

// breakpointManager returns the breakpoint manager, or answers the client.
func (s *Server) breakpointManager(w http.ResponseWriter) *breakpoint.Manager {
	m := s.core.Pipeline.Breakpoints()
	if m == nil {
		writeError(w, http.StatusNotImplemented, "breakpoints are not enabled in this core")
		return nil
	}
	return m
}

// ruleEngine returns the rule engine, or answers the client.
func (s *Server) ruleEngine(w http.ResponseWriter) *rule.Engine {
	e := s.core.Pipeline.Rules()
	if e == nil {
		writeError(w, http.StatusNotImplemented, "rules are not enabled in this core")
		return nil
	}
	return e
}
