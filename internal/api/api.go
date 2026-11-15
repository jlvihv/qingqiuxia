// Package api is the core's control plane: an HTTP server a GUI, a script or a
// browser talks to while the proxy runs.
//
// The API is the only supported way to observe and steer a running core, which
// keeps the GUI replaceable: it never links the proxy, it asks the API. The
// routes are plain JSON over HTTP, plus server-sent events for live traffic.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
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
	// Logger receives the API's request failures; nil is silent.
	Logger *log.Logger
}

// Server is the control API.
type Server struct {
	core  Core
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
	ln, err := net.Listen("tcp", addr)
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
func toMessageJSON(m *msgMessage) messageJSON {
	out := messageJSON{
		Headers:     map[string][]string(m.Header),
		ContentType: m.Header.Get("Content-Type"),
		Truncated:   m.Truncated,
		Streaming:   m.Stream != nil,
	}
	if len(m.Body) > 0 {
		if IsTextBody(m.Header.Get("Content-Type")) {
			out.Body = string(m.Body)
			out.BodyEncoding = "text"
		} else {
			out.Body = base64Encode(m.Body)
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
	offset := intParam(r, "offset", 0)
	limit := intParam(r, "limit", 200)
	// A negative limit means all, which the store understands as -1.
	if limit == 0 {
		limit = 200
	}
	list := s.core.Pipeline.Sessions().List(offset, limit)
	out := make([]sessionJSON, 0, len(list))
	// A list is rendered newest first, which is what a traffic list shows.
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, toSessionJSON(list[i], false))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": out,
		"total":    s.core.Pipeline.Sessions().Len(),
		"dropped":  s.core.Pipeline.Sessions().Dropped(),
	})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sessionID(w, r)
	if !ok {
		return
	}
	snap, found := s.core.Pipeline.Sessions().Get(id)
	if !found {
		writeError(w, http.StatusNotFound, "no session with that id")
		return
	}
	writeJSON(w, http.StatusOK, toSessionJSON(snap, true))
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
