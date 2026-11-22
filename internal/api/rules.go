package api

import (
	"encoding/base64"
	"net/http"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/rule"
)

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
//
// It carries both what a reader wants and what a writer needs: Effects is a
// sentence for a person or a model reading a list, and Spec is the rule as it
// was written, so that a caller can change one field and write it back. Without
// the second, reading a rule is not enough to edit it.
//
// Effects is marked read-only for decoding: it is what the server computed, and
// a caller that reads a rule and writes it back sends it along. Rejecting it as
// an unknown field made the documented read-modify-write round trip fail with a
// 400.
type ruleView struct {
	rule.Spec
	Effects []string `json:"effects,omitempty"`
}

// toRuleView describes a rule.
func toRuleView(r *rule.Rule) ruleView {
	return ruleView{Spec: r.Spec(), Effects: r.Describe()}
}

// decodeRuleSpec reads one rule from a request body, ignoring the fields the
// server computes and fills in itself. A caller may send a rule it just read.
func decodeRuleSpec(w http.ResponseWriter, r *http.Request) (rule.Spec, bool) {
	var body struct {
		rule.Spec
		// Effects and the other computed fields are accepted and dropped.
		Effects []string `json:"effects"`
	}
	if !decode(w, r, &body) {
		return rule.Spec{}, false
	}
	return body.Spec, true
}

// decodeRuleList reads a {"rules": [...]} body, ignoring the computed fields, so
// that a list read from GET can be written back after reordering it.
func decodeRuleList(w http.ResponseWriter, r *http.Request) ([]rule.Spec, bool) {
	var body struct {
		Rules []struct {
			rule.Spec
			Effects []string `json:"effects"`
		} `json:"rules"`
	}
	if !decode(w, r, &body) {
		return nil, false
	}
	out := make([]rule.Spec, 0, len(body.Rules))
	for _, item := range body.Rules {
		out = append(out, item.Spec)
	}
	return out, true
}

// handleReplaceRules swaps the whole rule set, which is how a GUI reorders
// rules.
func (s *Server) handleReplaceRules(w http.ResponseWriter, r *http.Request) {
	e := s.ruleEngine(w)
	if e == nil {
		return
	}
	specs, ok := decodeRuleList(w, r)
	if !ok {
		return
	}
	if err := e.Replace(specs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.rulesChanged(e)
	s.handleListRules(w, r)
}

// rulesChanged tells the core to write the rules down now, so that a crash does
// not cost someone the rules they just wrote.
func (s *Server) rulesChanged(e *rule.Engine) {
	if s.core.RulesChanged == nil {
		return
	}
	s.core.RulesChanged(rule.Specs(e.Rules()))
}

// handleGetRule returns one rule, which a caller reads before changing it.
func (s *Server) handleGetRule(w http.ResponseWriter, r *http.Request) {
	e := s.ruleEngine(w)
	if e == nil {
		return
	}
	id, ok := ruleID(w, r)
	if !ok {
		return
	}
	for _, rr := range e.Rules() {
		if rr.ID == id {
			writeJSON(w, http.StatusOK, toRuleView(rr))
			return
		}
	}
	writeError(w, http.StatusNotFound, "没有这个 id 的规则")
}

// handleAddRule appends one rule.
func (s *Server) handleAddRule(w http.ResponseWriter, r *http.Request) {
	e := s.ruleEngine(w)
	if e == nil {
		return
	}
	spec, ok := decodeRuleSpec(w, r)
	if !ok {
		return
	}
	added, err := e.Add(spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.rulesChanged(e)
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
	s.rulesChanged(e)
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
	s.rulesChanged(e)
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
//
// The edit runs under the pause's own lock, through Edit: the message is the one
// the proxy will continue with, and a snapshot taken at the same time must not
// see half an edit.
func (s *Server) handleEditPause(w http.ResponseWriter, r *http.Request) {
	m := s.breakpointManager(w)
	if m == nil {
		return
	}
	id, ok := ruleID(w, r)
	if !ok {
		return
	}
	pending, ok := m.PendingByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "no pause with that id")
		return
	}

	var body struct {
		// Side is "request" or "response". Empty edits the side that stopped,
		// which is what a caller who does not name one means.
		Side    string              `json:"side,omitempty"`
		Headers map[string][]string `json:"headers,omitempty"`
		Body    *string             `json:"body,omitempty"`
		Status  *int                `json:"status,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}

	// Which side to edit: the named one, or the one that stopped.
	side := body.Side
	if side == "" {
		side = pending.Phase.String()
	}
	if side != "request" && side != "response" {
		writeError(w, http.StatusBadRequest, `side must be "request" or "response"`)
		return
	}
	if side == "response" && pending.Response == nil {
		writeError(w, http.StatusBadRequest, "this pause has no response")
		return
	}
	// The live pause is needed for the edit itself; it is fetched again rather
	// than reused from the copy above, which is not editable.
	live, ok := m.Live(id)
	if !ok {
		writeError(w, http.StatusNotFound, "no pause with that id")
		return
	}

	err := live.Edit(func(req *msg.Request, resp *msg.Response) error {
		target := &req.Message
		if side == "response" {
			target = &resp.Message
		}
		if body.Headers != nil {
			target.Header = http.Header(body.Headers)
		}
		if body.Body != nil {
			// An edited body is text; the content length follows it.
			target.SetBody([]byte(*body.Body))
		}
		if body.Status != nil && side == "response" {
			resp.Status = *body.Status
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
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
		name := s.core.CertificateFile
		if name == "" {
			name = "ca.crt"
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(ca)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"pem": string(ca),
		// The same certificate, one line, for a GUI that shows a fingerprint.
		"pemBase64": base64.StdEncoding.EncodeToString(ca),
	})
}

// rfc3339Nano is the timestamp layout the API uses.
const rfc3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
