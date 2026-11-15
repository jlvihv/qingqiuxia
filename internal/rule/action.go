package rule

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// Action is one edit a rule makes. A rule applies its actions in order; each
// one may change the request or response the next one sees.
type Action interface {
	// apply edits req or resp. Exactly one of them is set: req for a request
	// rule, resp for a response rule. It reports whether it changed anything.
	apply(req *msg.Request, resp *msg.Response) (changed bool, err error)
	// describe names the action for the API and for a session's notes.
	describe() string
}

// ActionSpec is an action as it is written in the rules file and the API.
// One struct covers every action, since a rule has a few and the JSON stays
// readable; the fields an action does not use are ignored.
type ActionSpec struct {
	// Type is the action: set-url, set-query, set-header, remove-header,
	// set-body, replace-body, set-status, redirect.
	Type string `json:"type"`

	// Name is the header, query or form field the action targets. Empty means
	// the whole URL (set-url), the whole body (set-body, replace-body) or the
	// path (set-query with no name).
	Name string `json:"name,omitempty"`
	// Value is what to set, insert or replace with.
	Value string `json:"value,omitempty"`
	// Find and Replace drive replace-body. Find is a regular expression.
	Find    string `json:"find,omitempty"`
	Replace string `json:"replace,omitempty"`

	// Status is the response status a mock answers with, or set-status sets.
	Status int `json:"status,omitempty"`
	// ContentType sets the body's media type along with Value.
	ContentType string `json:"contentType,omitempty"`
}

// compileAction validates a spec and returns the action it describes. The
// result is immutable and safe to share.
func compileAction(spec ActionSpec) (Action, error) {
	typ := strings.ToLower(strings.TrimSpace(spec.Type))
	switch typ {
	case "set-url":
		if spec.Value == "" {
			return nil, fmt.Errorf("set-url needs a value")
		}
		return &setURL{value: spec.Value}, nil
	case "set-query":
		return &setQuery{name: spec.Name, value: spec.Value}, nil
	case "set-header":
		if spec.Name == "" {
			return nil, fmt.Errorf("set-header needs a name")
		}
		return &setHeader{name: http.CanonicalHeaderKey(spec.Name), value: spec.Value}, nil
	case "remove-header":
		if spec.Name == "" {
			return nil, fmt.Errorf("remove-header needs a name")
		}
		return &removeHeader{name: http.CanonicalHeaderKey(spec.Name)}, nil
	case "set-body":
		return &setBody{value: spec.Value, contentType: spec.ContentType}, nil
	case "replace-body":
		if spec.Find == "" {
			return nil, fmt.Errorf("replace-body needs a find pattern")
		}
		re, err := regexp.Compile(spec.Find)
		if err != nil {
			return nil, fmt.Errorf("replace-body: %w", err)
		}
		return &replaceBody{re: re, find: spec.Find, replace: spec.Replace}, nil
	case "set-status":
		if spec.Status < 100 || spec.Status > 599 {
			return nil, fmt.Errorf("set-status needs a status between 100 and 599")
		}
		return &setStatus{status: spec.Status}, nil
	case "redirect":
		if spec.Value == "" {
			return nil, fmt.Errorf("redirect needs a location")
		}
		if spec.Status == 0 {
			spec.Status = http.StatusFound
		}
		if spec.Status < 300 || spec.Status > 399 {
			return nil, fmt.Errorf("redirect needs a 3xx status, got %d", spec.Status)
		}
		return &redirect{location: spec.Value, status: spec.Status}, nil
	case "":
		return nil, fmt.Errorf("action needs a type")
	}
	return nil, fmt.Errorf("unknown action type %q", spec.Type)
}

// setURL replaces the request URL outright.
type setURL struct{ value string }

func (a *setURL) apply(req *msg.Request, _ *msg.Response) (bool, error) {
	u, err := url.Parse(a.value)
	if err != nil {
		return false, fmt.Errorf("set-url: %w", err)
	}
	if !u.IsAbs() {
		// A relative value replaces the path only, which is what a user
		// writing "/v2/users" means.
		u.Scheme, u.Host = req.URL.Scheme, req.URL.Host
		if u.Host == "" {
			u.Host = req.URL.Host
		}
	}
	req.URL = u
	return true, nil
}

func (a *setURL) describe() string { return "set-url " + a.value }

// setQuery sets, replaces or deletes one query parameter: an empty name with
// an empty value clears the query, an empty value with a name deletes it.
type setQuery struct{ name, value string }

func (a *setQuery) apply(req *msg.Request, _ *msg.Response) (bool, error) {
	q := req.URL.Query()
	switch {
	case a.name == "" && a.value == "":
		req.URL.RawQuery = ""
	case a.name == "":
		req.URL.RawQuery = strings.TrimPrefix(a.value, "?")
	case a.value == "":
		if _, ok := q[a.name]; !ok {
			return false, nil
		}
		q.Del(a.name)
		req.URL.RawQuery = q.Encode()
	default:
		if q.Get(a.name) == a.value && len(q[a.name]) == 1 {
			return false, nil
		}
		q.Set(a.name, a.value)
		req.URL.RawQuery = q.Encode()
	}
	return true, nil
}

func (a *setQuery) describe() string {
	if a.name == "" {
		return "set-query " + a.value
	}
	return fmt.Sprintf("set-query %s=%s", a.name, a.value)
}

// setHeader sets one header value on the request or the response.
type setHeader struct{ name, value string }

func (a *setHeader) apply(req *msg.Request, resp *msg.Response) (bool, error) {
	m := messageOf(req, resp)
	if m == nil {
		return false, nil
	}
	if m.Header.Get(a.name) == a.value {
		return false, nil
	}
	m.SetHeader(a.name, a.value)
	return true, nil
}

func (a *setHeader) describe() string { return fmt.Sprintf("set-header %s: %s", a.name, a.value) }

// removeHeader deletes a header.
type removeHeader struct{ name string }

func (a *removeHeader) apply(req *msg.Request, resp *msg.Response) (bool, error) {
	m := messageOf(req, resp)
	if m == nil || m.Header.Get(a.name) == "" {
		return false, nil
	}
	m.Header.Del(a.name)
	return true, nil
}

func (a *removeHeader) describe() string { return "remove-header " + a.name }

// setBody replaces a body with text.
//
// The text is encoded with the message's Content-Encoding, so that a client
// which asked for a compressed response still gets one it can decode: writing
// plain text under a Content-Encoding header breaks the response.
type setBody struct{ value, contentType string }

func (a *setBody) apply(req *msg.Request, resp *msg.Response) (bool, error) {
	m := messageOf(req, resp)
	if m == nil {
		return false, nil
	}
	if a.contentType != "" {
		m.SetHeader("Content-Type", a.contentType)
	}
	if !codecSupported(m.Header.Get("Content-Encoding")) {
		// An encoding the codec cannot reproduce: drop the header rather than
		// send bytes that claim to be something they are not.
		m.Header.Del("Content-Encoding")
	}
	return true, setEncodedBody(m, []byte(a.value))
}

func (a *setBody) describe() string { return "set-body" }

// replaceBody rewrites the decoded body text with a regular expression
// substitution, then restores the message's content coding.
type replaceBody struct {
	re      *regexp.Regexp
	find    string
	replace string
}

func (a *replaceBody) apply(req *msg.Request, resp *msg.Response) (bool, error) {
	m := messageOf(req, resp)
	if m == nil {
		return false, nil
	}
	text, ok := a.bodyText(m)
	if !ok {
		return false, nil
	}
	if !a.re.Match(text) {
		return false, nil
	}
	edited := a.re.ReplaceAll(text, []byte(expand(a.replace)))
	return true, setEncodedBody(m, edited)
}

// bodyText returns the body decoded to text, or false when it is not text the
// action may touch.
func (a *replaceBody) bodyText(m *msg.Message) ([]byte, bool) {
	if !m.Buffered() || m.Truncated || !msg.IsText(m.ContentType()) {
		return nil, false
	}
	if !codecSupported(m.Header.Get("Content-Encoding")) {
		return nil, false
	}
	// A body with no content encoding is by far the common case and needs no
	// decompression.
	if ce := m.Header.Get("Content-Encoding"); ce == "" || strings.EqualFold(ce, "identity") {
		return m.Body, true
	}
	decoded, err := decodeBody(m)
	if err != nil {
		return nil, false
	}
	return decoded, true
}

func (a *replaceBody) describe() string { return "replace-body /" + a.find + "/" }

// expand turns a replacement string's \1 and $1 into regexp's form, so that
// users may write whichever they are used to.
func expand(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			b.WriteString("${")
			b.WriteByte(s[i+1])
			b.WriteString("}")
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// setStatus sets a response status.
type setStatus struct{ status int }

func (a *setStatus) apply(_ *msg.Request, resp *msg.Response) (bool, error) {
	if resp == nil || resp.Status == a.status {
		return false, nil
	}
	resp.Status = a.status
	resp.Reason = http.StatusText(a.status)
	return true, nil
}

func (a *setStatus) describe() string { return "set-status " + strconv.Itoa(a.status) }

// redirect answers with a redirect, dropping the response body.
type redirect struct {
	location string
	status   int
}

func (a *redirect) apply(_ *msg.Request, resp *msg.Response) (bool, error) {
	if resp == nil {
		return false, nil
	}
	resp.Status = a.status
	resp.Reason = http.StatusText(a.status)
	resp.SetHeader("Location", a.location)
	resp.ClearBody()
	return true, nil
}

func (a *redirect) describe() string {
	return fmt.Sprintf("redirect %d %s", a.status, a.location)
}

// messageOf returns the side an action edits.
func messageOf(req *msg.Request, resp *msg.Response) *msg.Message {
	if req != nil {
		return &req.Message
	}
	if resp != nil {
		return &resp.Message
	}
	return nil
}

// JSONBody sets a body from a value, useful for mocks.
func JSONBody(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
