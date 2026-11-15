package proxy

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// httptraceTimes collects the origin connection's setup times, so a session can
// show where its time went.
//
// net/http calls the trace hooks from several goroutines of its own: the dial,
// the TLS handshake and the request's own path can overlap, so the fields are
// guarded.
type httptraceTimes struct {
	mu          sync.Mutex
	start       time.Time
	dnsDone     time.Time
	connectDone time.Time
	tlsDone     time.Time
}

// httptraceWithTimes attaches the collector to a context.
func httptraceWithTimes(ctx context.Context, t *httptraceTimes) context.Context {
	t.start = time.Now()
	trace := &httptrace.ClientTrace{
		DNSDone: func(httptrace.DNSDoneInfo) {
			t.mu.Lock()
			if t.dnsDone.IsZero() {
				t.dnsDone = time.Now()
			}
			t.mu.Unlock()
		},
		ConnectDone: func(_, _ string, err error) {
			if err != nil {
				return
			}
			t.mu.Lock()
			if t.connectDone.IsZero() {
				t.connectDone = time.Now()
			}
			t.mu.Unlock()
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err != nil {
				return
			}
			t.mu.Lock()
			if t.tlsDone.IsZero() {
				t.tlsDone = time.Now()
			}
			t.mu.Unlock()
		},
	}
	return httptrace.WithClientTrace(ctx, trace)
}

// fill copies the collected times onto the exchange. A reused connection has no
// setup to report, and leaves the fields at zero.
func (t *httptraceTimes) fill(ex *Exchange) {
	t.mu.Lock()
	start, dns, connect, tlsDone := t.start, t.dnsDone, t.connectDone, t.tlsDone
	t.mu.Unlock()
	if start.IsZero() {
		return
	}
	ex.mu.Lock()
	defer ex.mu.Unlock()
	if !dns.IsZero() {
		ex.timings.DNS = dns.Sub(start)
	}
	if !connect.IsZero() {
		base := start
		if !dns.IsZero() {
			base = dns
		}
		ex.timings.Connect = connect.Sub(base)
	}
	if !tlsDone.IsZero() {
		base := start
		if !connect.IsZero() {
			base = connect
		}
		ex.timings.TLS = tlsDone.Sub(base)
	}
}
