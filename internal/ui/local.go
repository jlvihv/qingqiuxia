package ui

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/codec"
	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/pipeline"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// Local is a Backend that calls the core in the same process.
//
// There is no serialisation and no socket here: a session that the proxy has
// just recorded is read as a value, and a change is handed to the window as one.
// That is what being in one process buys, and it is why the window does not talk
// to its own core over a network.
type Local struct {
	Pipeline *pipeline.Pipeline
	Proxy    *proxy.Proxy
	Version  string
	Started  time.Time
}

var _ Backend = (*Local)(nil)
var _ Control = (*Local)(nil)

// localSession converts a snapshot into the window's session.
func localSession(s session.Snapshot) Session {
	out := Session{
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
		Started:    s.Timings.Start,
		Error:      s.Err,
	}
	if r := s.Request; r != nil {
		out.Method = r.Method
		out.Proto = r.Proto
		if r.URL != nil {
			out.URL = r.URL.String()
			out.Host = r.URL.Host
		}
	}
	if r := s.Response; r != nil {
		out.Status = r.Status
	}
	for _, n := range s.Notes {
		out.Notes = append(out.Notes, Note{At: n.At, Rule: n.Rule, Kind: n.Kind, Text: n.Text})
	}
	return out
}

// ms turns a duration into milliseconds, the unit the window shows.
func ms(d time.Duration) float64 {
	if d == 0 {
		return 0
	}
	return float64(d.Microseconds()) / 1000
}

// Status describes the core.
func (l *Local) Status(context.Context) (Status, error) {
	running, addr := l.ProxyStatus()
	st := Status{
		Version:   l.Version,
		ProxyAddr: addr,
		Sessions:  l.Pipeline.Sessions().Len(),
		Rules:     l.Pipeline.Rules().Len(),
	}
	if m := l.Pipeline.Breakpoints(); m != nil {
		st.Paused = m.PauseCount()
	}
	if !running {
		st.ProxyAddr = ""
	}
	return st, nil
}

// Sessions returns the traffic, newest first.
//
// A limit asks for the newest rows, not the first ones: List pages from the
// oldest end, so a limit of three out of ten used to return the three that
// arrived first — the opposite of what a live list wants.
func (l *Local) Sessions(_ context.Context, limit int) ([]Session, int, uint64, error) {
	store := l.Pipeline.Sessions()
	offset := 0
	if limit > 0 {
		if n := store.Len(); n > limit {
			offset = n - limit
		}
	}
	list := store.List(offset, limit)
	out := make([]Session, 0, len(list))
	// The store lists oldest first; the window shows newest first.
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, localSession(list[i]))
	}
	return out, store.Len(), store.Dropped(), nil
}

// Pauses lists what is waiting at a breakpoint.
func (l *Local) Pauses(context.Context) ([]Pause, error) {
	m := l.Pipeline.Breakpoints()
	if m == nil {
		return nil, nil
	}
	pending := m.Pending()
	out := make([]Pause, 0, len(pending))
	for _, p := range pending {
		if p.Request != nil && p.Request.URL != nil && p.Request.Method != "" {
			out = append(out, toPause(p.ID, p.SessionID, p.Phase.String(), p.RuleName,
				p.Request.Method, p.Request.URL.String()))
			continue
		}
		out = append(out, toPause(p.ID, p.SessionID, p.Phase.String(), p.RuleName, "", ""))
	}
	return out, nil
}

// toPause builds a pause for the window.
func toPause(id, sessionID uint64, phase, rule, method, url string) Pause {
	return Pause{
		ID: id, SessionID: sessionID, Phase: phase,
		RuleName: rule, Method: method, URL: url,
	}
}

// Resume releases a paused message, or refuses it.
func (l *Local) Resume(_ context.Context, pauseID uint64, abort bool) error {
	m := l.Pipeline.Breakpoints()
	if m == nil {
		return nil
	}
	d := breakpoint.Release
	if abort {
		d = breakpoint.Abort
	}
	m.Resume(pauseID, d)
	return nil
}

// Watch streams changes to fn until ctx is cancelled.
//
// It subscribes to the stores directly, which is what a stream over a socket
// would carry as JSON: here the values arrive as they are.
func (l *Local) Watch(ctx context.Context, fn func(Change)) error {
	sessions := l.Pipeline.Sessions().Hub().Subscribe()
	defer sessions.Close()

	// Breakpoints are optional, so the subscription may be absent; a nil
	// channel in a select is simply never ready.
	var pauseC <-chan breakpoint.Event
	if m := l.Pipeline.Breakpoints(); m != nil {
		sub := m.Hub().Subscribe()
		defer sub.Close()
		pauseC = sub.C
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-sessions.C:
			if !ok {
				return nil
			}
			sess := localSession(ev.Session)
			kind := ChangeSession
			if ev.Kind == session.EventDropped {
				kind = ChangeDropped
			}
			fn(Change{Kind: kind, Session: &sess, Added: ev.Kind == session.EventNew})
		case ev, ok := <-pauseC:
			if !ok {
				return nil
			}
			var kind ChangeKind
			if ev.Kind == breakpoint.EventResumed {
				kind = ChangeResumed
			} else {
				kind = ChangePaused
			}
			p := toPause(ev.Paused.ID, ev.Paused.SessionID, ev.Paused.Phase.String(),
				ev.Paused.RuleName, methodOf(ev.Paused), urlOf(ev.Paused))
			fn(Change{Kind: kind, Pause: &p})
		}
	}
}

// methodOf and urlOf read a paused message's request.
func methodOf(p *breakpoint.Paused) string {
	if p.Request != nil {
		return p.Request.Method
	}
	return ""
}

func urlOf(p *breakpoint.Paused) string {
	if p.Request != nil && p.Request.URL != nil {
		return p.Request.URL.String()
	}
	return ""
}

// ProxyStatus says whether the proxy is running and where.
func (l *Local) ProxyStatus() (bool, string) {
	return l.Proxy.Running(), l.Proxy.Addr()
}

// StartProxy listens on addr and begins serving.
func (l *Local) StartProxy(addr string) (string, error) {
	return l.Proxy.Start(addr)
}

// StopProxy stops serving and releases the port.
func (l *Local) StopProxy() error {
	return l.Proxy.Stop()
}

// Detail returns one session's headers and bodies.
//
// The body is decoded for reading: what a user wants from a gzipped response is
// the JSON inside it, not the compressed bytes. The coding is named in a header
// the panel shows, so nothing is hidden.
func (l *Local) Detail(_ context.Context, id uint64) (Detail, error) {
	snap, ok := l.Pipeline.Sessions().Get(id)
	if !ok {
		return Detail{}, fmt.Errorf("no session %d", id)
	}
	var d Detail
	if snap.Request != nil {
		d.Request = messageOf(&snap.Request.Message)
	}
	if snap.Response != nil {
		d.Response = messageOf(&snap.Response.Message)
	}
	return d, nil
}

// messageOf turns a message into what the panel shows.
func messageOf(m *msg.Message) Message {
	out := Message{
		ContentType: m.Header.Get("Content-Type"),
		Truncated:   m.Truncated,
		Streaming:   m.Stream != nil,
		Size:        int64(len(m.Body)),
	}
	out.Headers = headersOf(m.Header)
	if len(m.Body) == 0 {
		return out
	}
	// A body that is compressed is decoded for reading; one whose coding cannot
	// be undone is reported as it is rather than shown as noise.
	body := m.Body
	if ce := m.Header.Get("Content-Encoding"); ce != "" && !strings.EqualFold(ce, "identity") {
		if decoded, err := codec.Decode(body, ce); err == nil {
			body = decoded
		}
	}
	if msg.IsText(msg.MediaType(out.ContentType)) && utf8.Valid(body) {
		out.Body = string(body)
		return out
	}
	out.Binary = true
	out.Base64 = base64.StdEncoding.EncodeToString(body)
	return out
}

// headersOf lists a header's lines in a stable order, so a panel does not
// reshuffle them between frames.
func headersOf(h http.Header) []Header {
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]Header, 0, len(names))
	for _, k := range names {
		for _, v := range h[k] {
			out = append(out, Header{Name: k, Value: v})
		}
	}
	return out
}
