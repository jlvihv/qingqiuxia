package api

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// msgMessage is the message type the API reads headers and bodies from. It is
// an alias so that the API's helpers read as if they took the model directly.
type msgMessage = msg.Message

// IsTextBody reports whether a body is worth sending as text.
func IsTextBody(contentType string) bool { return msg.IsText(msg.MediaType(contentType)) }

func base64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// handleListRules returns the rules.
func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	e := s.ruleEngine(w)
	if e == nil {
		return
	}
	rules := e.Rules()
	out := make([]ruleView, 0, len(rules))
	for _, rr := range rules {
		out = append(out, toRuleView(rr))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": out})
}

// ruleView is how the API shows a rule.
type ruleView struct {
	ID      uint64   `json:"id"`
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Kind    string   `json:"kind"`
	Phase   string   `json:"phase"`
	Pattern string   `json:"pattern"`
	Effects []string `json:"effects,omitempty"`
	// Matched counts what the rule has changed since the core started.
	Matched int64 `json:"matched"`
}

func toRuleView(r *rule.Rule) ruleView {
	return ruleView{
		ID:      r.ID,
		Name:    r.Name,
		Enabled: r.Enabled,
		Kind:    r.Kind.String(),
		Phase:   r.Phase.String(),
		Pattern: r.Matcher.String(),
		Effects: r.Describe(),
	}
}

// handleReplaceRules swaps the whole rule set, which is how a GUI reorders
// rules.
func (s *Server) handleReplaceRules(w http.ResponseWriter, r *http.Request) {
	e := s.ruleEngine(w)
	if e == nil {
		return
	}
	var body struct {
		Rules []rule.Spec `json:"rules"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := e.Replace(body.Rules); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.handleListRules(w, r)
}

// handleAddRule appends one rule.
func (s *Server) handleAddRule(w http.ResponseWriter, r *http.Request) {
	e := s.ruleEngine(w)
	if e == nil {
		return
	}
	var spec rule.Spec
	if !decode(w, r, &spec) {
		return
	}
	added, err := e.Add(spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toRuleView(added))
}

// handlePatchRule turns a rule on or off, or updates it wholesale.
func (s *Server) handlePatchRule(w http.ResponseWriter, r *http.Request) {
	e := s.ruleEngine(w)
	if e == nil {
		return
	}
	id, ok := ruleID(w, r)
	if !ok {
		return
	}
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
	updated, err := e.SetEnabled(id, *body.Enabled)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toRuleView(updated))
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	e := s.ruleEngine(w)
	if e == nil {
		return
	}
	id, ok := ruleID(w, r)
	if !ok {
		return
	}
	if err := e.Delete(id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListBreakpoints returns the breakpoint rules.
func (s *Server) handleListBreakpoints(w http.ResponseWriter, r *http.Request) {
	m := s.breakpointManager(w)
	if m == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"breakpoints": m.Rules()})
}

// handleReplaceBreakpoints swaps the breakpoint rules.
func (s *Server) handleReplaceBreakpoints(w http.ResponseWriter, r *http.Request) {
	m := s.breakpointManager(w)
	if m == nil {
		return
	}
	var body struct {
		Breakpoints []breakpoint.Spec `json:"breakpoints"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := m.Replace(body.Breakpoints); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.handleListBreakpoints(w, r)
}

// pausedJSON is how the API shows a paused message.
type pausedJSON struct {
	ID        uint64       `json:"id"`
	SessionID uint64       `json:"sessionId"`
	Phase     string       `json:"phase"`
	RuleName  string       `json:"ruleName"`
	At        string       `json:"at"`
	Request   *messageJSON `json:"request,omitempty"`
	Response  *messageJSON `json:"response,omitempty"`
	// URL and Method are the request's, for a list a GUI shows without asking
	// for the detail.
	Method string `json:"method,omitempty"`
	URL    string `json:"url,omitempty"`
}

// handleListPauses returns the messages waiting for a decision.
func (s *Server) handleListPauses(w http.ResponseWriter, r *http.Request) {
	m := s.breakpointManager(w)
	if m == nil {
		return
	}
	pending := m.Pending()
	out := make([]pausedJSON, 0, len(pending))
	for _, p := range pending {
		out = append(out, toPausedJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pauses": out})
}

// handleResume releases a paused message, after applying any edits.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	m := s.breakpointManager(w)
	if m == nil {
		return
	}
	id, ok := ruleID(w, r)
	if !ok {
		return
	}
	if !m.Resume(id, breakpoint.Release) {
		writeError(w, http.StatusNotFound, "no pause with that id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"resumed": true})
}

// handleAbortPause refuses a paused message.
func (s *Server) handleAbortPause(w http.ResponseWriter, r *http.Request) {
	m := s.breakpointManager(w)
	if m == nil {
		return
	}
	id, ok := ruleID(w, r)
	if !ok {
		return
	}
	if !m.Resume(id, breakpoint.Abort) {
		writeError(w, http.StatusNotFound, "no pause with that id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"aborted": true})
}

// handleEditPause changes a paused message's headers or body.
func (s *Server) handleEditPause(w http.ResponseWriter, r *http.Request) {
	m := s.breakpointManager(w)
	if m == nil {
		return
	}
	id, ok := ruleID(w, r)
	if !ok {
		return
	}
	pending := m.Pending()
	var target *breakpoint.Paused
	for _, p := range pending {
		if p.ID == id {
			target = p
			break
		}
	}
	if target == nil {
		writeError(w, http.StatusNotFound, "no pause with that id")
		return
	}

	var body struct {
		// Side is "request" or "response"; it defaults to the paused side.
		Side    string              `json:"side,omitempty"`
		Headers map[string][]string `json:"headers,omitempty"`
		Body    *string             `json:"body,omitempty"`
		Status  *int                `json:"status,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}

	var m2 *msg.Message
	var resp *msg.Response
	switch body.Side {
	case "", "request":
		if target.Phase == breakpoint.OnResponse && body.Side == "" {
			m2 = &target.Response.Message
			resp = target.Response
		} else {
			m2 = &target.Request.Message
		}
	case "response":
		if target.Response == nil {
			writeError(w, http.StatusBadRequest, "this pause has no response")
			return
		}
		m2 = &target.Response.Message
		resp = target.Response
	}
	if m2 == nil {
		writeError(w, http.StatusBadRequest, "nothing to edit")
		return
	}
	if body.Headers != nil {
		m2.Header = http.Header(body.Headers)
	}
	if body.Body != nil {
		// An edited body is text; the content length follows it.
		m2.SetBody([]byte(*body.Body))
	}
	if body.Status != nil && resp != nil {
		resp.Status = *body.Status
	}
	writeJSON(w, http.StatusOK, map[string]bool{"edited": true})
}

// handleCertificate returns the authority certificate for the client to
// install, as a PEM download or as JSON with its path.
func (s *Server) handleCertificate(w http.ResponseWriter, r *http.Request) {
	ca := s.core.Pipeline.CertificatePEM()
	if ca == nil {
		writeError(w, http.StatusNotFound, "this core has no certificate authority")
		return
	}
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Header().Set("Content-Disposition", `attachment; filename="qingqiuxia-ca.crt"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ca)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"pem": string(ca),
		// The same certificate, one line, for a GUI that shows a fingerprint.
		"pemBase64": base64Encode(ca),
	})
}

// rfc3339Nano is the timestamp layout the API uses.
const rfc3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

var _ = strings.TrimSpace
var _ = session.Pending
