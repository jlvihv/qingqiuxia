package proxy

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// Timings is where an exchange's time went: the origin connection's setup and
// the wait for the first response byte.
//
// It is exported because the policy records it on the session, which is what
// the traffic list shows.
type Timings struct {
	// DNS, Connect and TLS are the origin connection's setup, zero when a
	// connection was reused.
	DNS, Connect, TLS time.Duration
	// TTFB is from the start of the request to the first body byte.
	TTFB time.Duration
}

// IsZero reports whether nothing was recorded, which is the case for a reused
// connection whose response the caller did not measure.
func (t Timings) IsZero() bool {
	return t.DNS == 0 && t.Connect == 0 && t.TLS == 0 && t.TTFB == 0
}

// httptraceTimes collects the origin connection's setup times.
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

// collect returns the setup times, with ttfb from the caller's own clock.
func (t *httptraceTimes) collect(ttfb time.Duration) Timings {
	t.mu.Lock()
	start, dns, connect, tlsDone := t.start, t.dnsDone, t.connectDone, t.tlsDone
	t.mu.Unlock()

	out := Timings{TTFB: ttfb}
	if start.IsZero() {
		return out
	}
	if !dns.IsZero() {
		out.DNS = dns.Sub(start)
	}
	if !connect.IsZero() {
		base := start
		if !dns.IsZero() {
			base = dns
		}
		out.Connect = connect.Sub(base)
	}
	if !tlsDone.IsZero() {
		base := start
		if !connect.IsZero() {
			base = connect
		}
		out.TLS = tlsDone.Sub(base)
	}
	return out
}
