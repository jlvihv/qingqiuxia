package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/events"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// handleEvents streams traffic changes as server-sent events, which is how a
// GUI keeps its list live without polling.
//
// Each event is one JSON object on a "data:" line; the "event:" line names the
// kind, so a client can filter without parsing the payload.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported by this connection")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	store := s.core.Pipeline.Sessions()
	sub := store.Hub().Subscribe()
	defer sub.Close()

	// Breakpoint pauses travel on the same stream, so a GUI needs one
	// connection to know about both traffic and what is waiting for it.
	pauseSub := (*events.Subscription[breakpoint.Event])(nil)
	if pauses := s.core.Pipeline.Breakpoints(); pauses != nil {
		pauseSub = pauses.Hub().Subscribe()
		defer pauseSub.Close()
	}

	// Tell the client it is connected, so it can tell an idle core from a
	// stalled one.
	if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	// Keep the connection warm: a proxy in front of the API, or a browser, may
	// close an idle stream.
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-sub.C:
			if !ok {
				return
			}
			payload := toSessionJSON(ev.Session, false)
			if err := writeEvent(w, eventName(ev.Kind), payload); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-pauseChan(pauseSub):
			if !ok {
				return
			}
			if err := writeEvent(w, pauseEventName(ev.Kind), toPausedJSON(ev.Paused)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// pauseChan returns the pause subscription's channel, or nil when breakpoints
// are off, so the select above simply never takes that case.
func pauseChan(sub *events.Subscription[breakpoint.Event]) <-chan breakpoint.Event {
	if sub == nil {
		return nil
	}
	return sub.C
}

// pauseEventName maps a breakpoint event to its SSE name.
func pauseEventName(k breakpoint.EventKind) string {
	if k == breakpoint.EventResumed {
		return "resumed"
	}
	return "paused"
}

// toPausedJSON describes a pause for the stream.
func toPausedJSON(p *breakpoint.Paused) pausedJSON {
	v := pausedJSON{
		ID:        p.ID,
		SessionID: p.SessionID,
		Phase:     p.Phase.String(),
		RuleName:  p.RuleName,
		At:        p.At.Format(rfc3339Nano),
	}
	if p.Request != nil {
		v.Method = p.Request.Method
		if p.Request.URL != nil {
			v.URL = p.Request.URL.String()
		}
		m := toMessageJSON(&p.Request.Message)
		v.Request = &m
	}
	if p.Response != nil {
		m := toMessageJSON(&p.Response.Message)
		v.Response = &m
	}
	return v
}

// eventName maps a session event to its SSE name.
func eventName(k session.EventKind) string {
	switch k {
	case session.EventNew:
		return "session"
	case session.EventUpdate, session.EventDone:
		return "session"
	case session.EventDropped:
		return "dropped"
	}
	return "session"
}

// writeEvent writes one server-sent event.
func writeEvent(w io.Writer, name string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
	return err
}

// Repeat sends a recorded request again, as a capture tool's replay does.
func (s *Server) handleRepeat(w http.ResponseWriter, r *http.Request) {
	store := s.core.Pipeline.Sessions()
	id, ok := s.sessionID(w, r)
	if !ok {
		return
	}
	snap, found := store.Get(id)
	if !found {
		writeError(w, http.StatusNotFound, "no session with that id")
		return
	}
	var body struct {
		// Count is how many times to send; 1 by default, at most 100.
		Count int `json:"count,omitempty"`
		// Delay before the first send, in milliseconds.
		Delay int `json:"delay,omitempty"`
		// Interval between sends, in milliseconds.
		Interval int `json:"interval,omitempty"`
	}
	// A repeat with no body is still a repeat once.
	if r.ContentLength > 0 {
		if !decode(w, r, &body) {
			return
		}
	}
	if body.Count <= 0 {
		body.Count = 1
	}
	if body.Count > 100 {
		writeError(w, http.StatusBadRequest, "count is at most 100")
		return
	}
	if s.core.Repeat == nil {
		writeError(w, http.StatusNotImplemented, "repeating is not enabled in this core")
		return
	}
	go s.core.Repeat(snap, body.Count, time.Duration(body.Delay)*time.Millisecond, time.Duration(body.Interval)*time.Millisecond)
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": body.Count})
}
