package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client talks to the core's control API.
//
// The window holds one of these and nothing else knows the API's shape: the
// model and the view work with Session and Paused, so the transport can change
// without touching them.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient returns a client for a core listening at addr, as "127.0.0.1:8898".
func NewClient(addr, token string) *Client {
	return &Client{
		base:  "http://" + addr,
		token: token,
		http:  &http.Client{Timeout: 20 * time.Second},
	}
}

// do sends a request and decodes its JSON body into out, which may be nil.
func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("%s %s: %s", method, path, e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// wireSession is the API's shape for a session, kept apart from Session so that
// a change on either side is a compile error here rather than a silent zero.
type wireSession struct {
	ID     uint64 `json:"id"`
	Phase  string `json:"phase"`
	Source string `json:"source"`
	Method string `json:"method"`
	URL    string `json:"url"`
	Host   string `json:"host"`
	Proto  string `json:"proto"`
	Status int    `json:"status"`
	Error  string `json:"error"`

	DurationMS float64 `json:"durationMs"`
	TTFBMS     float64 `json:"ttfbMs"`
	DNSMS      float64 `json:"dnsMs"`
	ConnectMS  float64 `json:"connectMs"`
	TLSMS      float64 `json:"tlsMs"`

	BytesIn   int64  `json:"bytesIn"`
	BytesOut  int64  `json:"bytesOut"`
	StartedAt string `json:"startedAt"`

	Notes []struct {
		At   string `json:"at"`
		Rule string `json:"rule"`
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"notes"`
}

// toSession turns the API's shape into the window's.
func (w *wireSession) toSession() Session {
	s := Session{
		ID:         w.ID,
		Phase:      w.Phase,
		Source:     w.Source,
		Method:     w.Method,
		URL:        w.URL,
		Host:       w.Host,
		Proto:      w.Proto,
		Status:     w.Status,
		Error:      w.Error,
		DurationMS: w.DurationMS,
		TTFBMS:     w.TTFBMS,
		DNSMS:      w.DNSMS,
		ConnectMS:  w.ConnectMS,
		TLSMS:      w.TLSMS,
		BytesIn:    w.BytesIn,
		BytesOut:   w.BytesOut,
	}
	if t, err := time.Parse(time.RFC3339Nano, w.StartedAt); err == nil {
		s.Started = t
	}
	for _, n := range w.Notes {
		note := Note{Rule: n.Rule, Kind: n.Kind, Text: n.Text}
		if t, err := time.Parse(time.RFC3339Nano, n.At); err == nil {
			note.At = t
		}
		s.Notes = append(s.Notes, note)
	}
	return s
}

// Status is what the core says about itself.
type Status struct {
	Version    string `json:"version"`
	ProxyAddr  string `json:"proxyAddr"`
	APIVersion int    `json:"apiVersion"`
	Capturing  bool   `json:"capturing"`
	Sessions   int    `json:"sessions"`
	Rules      int    `json:"rules"`
	Paused     int    `json:"paused"`
	MemoryMB   uint64 `json:"memoryMb"`
	Goroutines int    `json:"goroutines"`
}

// Status fetches the core's status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	err := c.do(ctx, http.MethodGet, "/api/status", &st)
	return st, err
}

// Sessions fetches the traffic list, newest first.
//
// limit of 0 asks for the API's default page; a negative limit asks for all of
// them, which is what the window does when it opens, so that the list starts
// with the traffic the core already holds.
func (c *Client) Sessions(ctx context.Context, limit int) ([]Session, int, error) {
	path := "/api/sessions"
	if limit != 0 {
		path = fmt.Sprintf("/api/sessions?limit=%d", limit)
	}
	var body struct {
		Sessions []wireSession `json:"sessions"`
		Total    int           `json:"total"`
		Dropped  uint64        `json:"dropped"`
	}
	if err := c.do(ctx, http.MethodGet, path, &body); err != nil {
		return nil, 0, err
	}
	out := make([]Session, 0, len(body.Sessions))
	for i := range body.Sessions {
		out = append(out, body.Sessions[i].toSession())
	}
	return out, body.Total, nil
}

// Counts fetches how many sessions the core holds and how many it dropped.
func (c *Client) Counts(ctx context.Context) (total int, dropped uint64, err error) {
	var body struct {
		Total   int    `json:"total"`
		Dropped uint64 `json:"dropped"`
	}
	err = c.do(ctx, http.MethodGet, "/api/sessions?limit=1", &body)
	return body.Total, body.Dropped, err
}

// SetCapture turns capture on or off.
func (c *Client) SetCapture(ctx context.Context, on bool) error {
	return c.postJSON(ctx, "/api/capture", map[string]any{"enabled": on}, nil)
}

// ClearSessions empties the core's traffic list.
func (c *Client) ClearSessions(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/api/sessions", nil)
}

// Repeat sends a recorded session again.
func (c *Client) Repeat(ctx context.Context, id uint64, count int) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/sessions/%d/repeat", id),
		map[string]any{"count": count}, nil)
}

// Pause is a message held at a breakpoint, as the API describes it.
type Pause struct {
	ID        uint64 `json:"id"`
	SessionID uint64 `json:"sessionId"`
	Phase     string `json:"phase"`
	RuleName  string `json:"ruleName"`
	Method    string `json:"method"`
	URL       string `json:"url"`
}

// Pauses lists what is waiting at a breakpoint.
func (c *Client) Pauses(ctx context.Context) ([]Pause, error) {
	var body struct {
		Pauses []Pause `json:"pauses"`
	}
	err := c.do(ctx, http.MethodGet, "/api/pauses", &body)
	return body.Pauses, err
}

// Resume releases a paused message.
func (c *Client) Resume(ctx context.Context, id uint64) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/pauses/%d/resume", id), nil, nil)
}

// AbortPause refuses a paused message.
func (c *Client) AbortPause(ctx context.Context, id uint64) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/pauses/%d/abort", id), nil, nil)
}

// postJSON sends a JSON body, or none when body is nil.
func (c *Client) postJSON(ctx context.Context, path string, body, out any) error {
	var rdr *strings.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = strings.NewReader(string(data))
	} else {
		rdr = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("POST %s: %s", path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Event is one server-sent event from the core.
type Event struct {
	// Name is the event's name: session, dropped, paused or resumed.
	Name string
	// Data is its JSON payload.
	Data []byte
}

// Stream connects to the core's event stream and calls fn for each event until
// the context is cancelled or the stream ends.
//
// The core publishes every change to a session, so the window stays live
// without polling. A dropped connection is reported so the caller can retry,
// which the window does: a core that restarts must not leave a stale list.
func (c *Client) Stream(ctx context.Context, fn func(Event)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/events", nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	// No timeout on the client for a stream: it is meant to stay open.
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("events: %s", resp.Status)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var ev Event
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			// A blank line ends an event.
			if ev.Name != "" && ev.Data != nil {
				fn(ev)
			}
			ev = Event{}
		case strings.HasPrefix(line, ": "):
			// A comment: the core sends these to keep the stream warm.
		case strings.HasPrefix(line, "event: "):
			ev.Name = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			ev.Data = append(ev.Data, line[len("data: "):]...)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return fmt.Errorf("events: the stream ended")
}

// Live feeds a store from the core: it loads what the core already holds, then
// follows the stream, and reloads from scratch whenever the stream drops.
//
// It runs until ctx is cancelled, and is meant to be started in its own
// goroutine, with invalidate called to ask for a frame when the store changes.
func (c *Client) Live(ctx context.Context, store *Store, invalidate func(), appendHint func()) {
	for ctx.Err() == nil {
		if err := c.load(ctx, store, invalidate); err != nil {
			// A core that is not running yet is not an error worth showing:
			// the window waits for it, as it waits for a proxy to start.
			sleep(ctx, time.Second)
			continue
		}
		if err := c.Stream(ctx, func(ev Event) {
			appended := c.apply(ev, store)
			if appended {
				// An arriving session only extends the list: the window does
				// not need to refilter thousands of rows for it.
				appendHint()
			} else {
				invalidate()
			}
		}); err != nil {
			sleep(ctx, 500*time.Millisecond)
		}
	}
}

// load pulls the core's current sessions into the store.
func (c *Client) load(ctx context.Context, store *Store, invalidate func()) error {
	sessions, total, err := c.Sessions(ctx, -1)
	if err != nil {
		return err
	}
	store.Clear()
	// The API lists newest first; the store inserts at the front, so replay the
	// list backwards to keep the same order.
	for i := len(sessions) - 1; i >= 0; i-- {
		store.Upsert(sessions[i])
	}
	_, dropped, err := c.Counts(ctx)
	if err == nil {
		store.SetCounts(total, dropped)
	} else {
		store.SetCounts(total, 0)
	}
	invalidate()
	return nil
}

// apply folds one streamed event into the store, reporting whether it only
// appended a new session at the newest end.
func (c *Client) apply(ev Event, store *Store) (appended bool) {
	switch ev.Name {
	case "session":
		var w wireSession
		if json.Unmarshal(ev.Data, &w) != nil {
			return false
		}
		sess := w.toSession()
		if store.Has(w.ID) {
			// The same session, now complete: its row changes in place.
			store.Upsert(sess)
			return false
		}
		store.Upsert(sess)
		return true
	case "dropped":
		var w wireSession
		if json.Unmarshal(ev.Data, &w) == nil {
			store.Remove(w.ID)
		}
	}
	return false
}

// sleep waits for d, or until ctx is cancelled.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
