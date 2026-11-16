// Package pipeline is the proxy's policy: it records every exchange in the
// session store, runs the rules and the breakpoints, and reports to the GUI
// why a message looks the way it does.
//
// It is the only place that knows about all three, which keeps the proxy's
// network code and the API's transport code free of policy.
package pipeline

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// Options configures a pipeline.
type Options struct {
	// Rules decides what to do with matching requests and responses.
	Rules *rule.Engine
	// Breakpoints pauses matching traffic for a person to decide on.
	Breakpoints *breakpoint.Manager
	// Sessions records the traffic.
	Sessions *session.Store
	// Issuer mints the certificates for intercepted TLS connections.
	Issuer CertificateIssuer
	// ShouldIntercept decides whether a TLS connection is decrypted. It is
	// called for every CONNECT, so it must be cheap.
	ShouldIntercept func(host string) bool
}

// CertificateIssuer mints leaf certificates; *mitm.Issuer implements it.
type CertificateIssuer interface {
	TLSCertFor(host string) (*tls.Certificate, error)
	// PEM returns the authority certificate, for the client to install.
	PEM() []byte
}

// CertificatePEM returns the authority certificate, or nil when there is none.
func (p *Pipeline) CertificatePEM() []byte {
	if p.issuer == nil {
		return nil
	}
	return p.issuer.PEM()
}

// Pipeline is the proxy's policy. It is safe for concurrent use.
type Pipeline struct {
	rules       *rule.Engine
	breakpoints *breakpoint.Manager
	sessions    *session.Store
	issuer      CertificateIssuer
	intercept   func(host string) bool

	// sessions maps an exchange to the session it records into. The proxy
	// hands the exchange back unchanged, so the ID lives here rather than in
	// the exchange, which keeps the proxy's type free of the store's.
	// recording reports whether new exchanges are recorded.
	recording atomic.Bool
}

// New returns a pipeline with the given options.
func New(opts Options) *Pipeline {
	p := &Pipeline{
		rules:       opts.Rules,
		breakpoints: opts.Breakpoints,
		sessions:    opts.Sessions,
		issuer:      opts.Issuer,
		intercept:   opts.ShouldIntercept,
	}
	if p.rules == nil {
		p.rules = rule.NewEngine()
	}
	if p.sessions == nil {
		p.sessions = session.NewStore(0)
	}
	p.recording.Store(true)
	return p
}

// SetRecording turns recording of new exchanges on or off. Traffic keeps
// flowing either way; only the session store stops filling.
func (p *Pipeline) SetRecording(on bool) { p.recording.Store(on) }

// Recording reports whether new exchanges are recorded.
func (p *Pipeline) Recording() bool { return p.recording.Load() }

// Rules returns the rule engine.
func (p *Pipeline) Rules() *rule.Engine { return p.rules }

// Breakpoints returns the breakpoint manager.
func (p *Pipeline) Breakpoints() *breakpoint.Manager { return p.breakpoints }

// Sessions returns the session store.
func (p *Pipeline) Sessions() *session.Store { return p.sessions }

// Intercepts reports whether a TLS connection to host is decrypted.
func (p *Pipeline) Intercepts(host string) bool {
	if p.intercept == nil || p.issuer == nil {
		return false
	}
	return p.intercept(host)
}

// Certificate returns the certificate to present for host.
func (p *Pipeline) Certificate(host string) (*tls.Certificate, error) {
	if p.issuer == nil {
		return nil, fmt.Errorf("pipeline: no certificate issuer configured")
	}
	return p.issuer.TLSCertFor(host)
}

// Begin records the start of an exchange, publishing the new session so a GUI
// shows the row while the request is still in flight.
func (p *Pipeline) Begin(ex *proxy.Exchange) {
	if !p.recording.Load() {
		return
	}
	sess := p.sessions.Begin(ex.Request, ex.Started)
	ex.ID = sess.ID
	// The exchange carries the session, so Request, Response and Complete need
	// no lookup and no lock: the policy's state travels with the exchange it
	// belongs to.
	ex.Policy = sess
}

// Request runs the request rules and breakpoints.
func (p *Pipeline) Request(ctx context.Context, ex *proxy.Exchange) (proxy.Decision, error) {
	sess := p.sessionFor(ex)
	var decision proxy.Decision

	out, err := p.rules.Request(ex.Request)
	if err != nil {
		// A rule that fails must not take the request down with it: the
		// original request goes through, and the failure is noted.
		if sess != nil {
			sess.AddNote(session.Note{At: time.Now(), Kind: "rule", Text: "request rules failed: " + err.Error()})
		}
	} else {
		decision.Delay = out.Delay
		if out.Mock != nil {
			decision.Mock = out.Mock
			if sess != nil {
				sess.SetSource(session.Mocked)
			}
		}
		if out.Abort {
			decision.Abort = true
			decision.Status = out.Status
			if sess != nil {
				sess.SetSource(session.Aborted)
			}
		}
		if sess != nil {
			p.recordApplied(sess, out.Applied)
			sess.SetRequest(ex.Request)
		}
	}

	if p.breakpoints == nil || decision.Mock != nil || decision.Abort {
		// A mock or an abort already answers the request: a request breakpoint
		// for it would change what nobody forwards.
		return decision, nil
	}
	// An explicit breakpoint stops the request even when no rule would have
	// changed it.
	ic, err := p.breakpoints.Intercept(ctx, ex.ID, breakpoint.OnRequest, ex.Request, nil)
	if err != nil {
		return decision, err
	}
	if ic.Matched {
		if ic.Abort {
			decision.Abort = true
			decision.Status = ic.Status
			if sess != nil {
				sess.SetSource(session.Aborted)
				sess.AddNote(session.Note{At: time.Now(), Kind: "breakpoint", Text: "aborted at a request breakpoint"})
			}
			return decision, nil
		}
		if ic.Request != nil {
			ex.Request = ic.Request
			if sess != nil {
				sess.SetRequest(ex.Request)
				sess.AddNote(session.Note{At: time.Now(), Kind: "breakpoint", Text: "edited at a request breakpoint"})
			}
		}
	}
	return decision, nil
}

// Response runs the response rules and breakpoints.
func (p *Pipeline) Response(ctx context.Context, ex *proxy.Exchange, resp *msg.Response) (*msg.Response, error) {
	sess := p.sessionFor(ex)
	applied, err := p.rules.Response(ex.Request, resp)
	if err != nil && sess != nil {
		sess.AddNote(session.Note{At: time.Now(), Kind: "rule", Text: "response rules failed: " + err.Error()})
	}
	if sess != nil {
		p.recordApplied(sess, applied)
	}

	if p.breakpoints == nil {
		return resp, nil
	}
	ic, err := p.breakpoints.Intercept(ctx, ex.ID, breakpoint.OnResponse, ex.Request, resp)
	if err != nil {
		return resp, err
	}
	if ic.Matched {
		if ic.Abort {
			// An abort on the response side replaces what the client would get:
			// the origin's response was already fetched, but forwarding it now
			// would ignore the decision the person made.
			if sess != nil {
				sess.SetSource(session.Aborted)
				sess.AddNote(session.Note{At: time.Now(), Kind: "breakpoint", Text: "aborted at a response breakpoint"})
			}
			return statusResponse(breakpoint.StatusForAbort), nil
		}
		if ic.Response != nil {
			if sess != nil {
				sess.AddNote(session.Note{At: time.Now(), Kind: "breakpoint", Text: "edited at a response breakpoint"})
			}
			return ic.Response, nil
		}
	}
	return resp, nil
}

// statusResponse is the empty response an abort answers with.
func statusResponse(status int) *msg.Response {
	if status == 0 {
		status = http.StatusForbidden
	}
	return &msg.Response{
		Status:  status,
		Reason:  http.StatusText(status),
		Message: msg.Message{Header: http.Header{}},
	}
}

// Complete records the end of an exchange.
func (p *Pipeline) Complete(ex *proxy.Exchange, resp *msg.Response, err error) {
	sess := p.sessionFor(ex)
	if sess == nil {
		return
	}
	// One call records the whole timing, rather than four setters that each
	// took the session's lock.
	t := ex.Timings()
	sess.SetTimings(session.Timings{DNS: t.DNS, Connect: t.Connect, TLS: t.TLS, TTFB: t.TTFB})
	sess.Complete(resp, err, time.Now())
	p.sessions.Publish(sess)
}

// sessionFor returns the session an exchange records into, which Begin put on
// the exchange itself.
func (p *Pipeline) sessionFor(ex *proxy.Exchange) *session.Session {
	if ex == nil {
		return nil
	}
	sess, _ := ex.Policy.(*session.Session)
	return sess
}

// recordApplied notes what the rules did, one note per rule.
func (p *Pipeline) recordApplied(sess *session.Session, applied []rule.Applied) {
	for _, a := range applied {
		sess.AddNote(session.Note{
			At:   time.Now(),
			Rule: a.Name,
			Kind: a.Kind,
			Text: joinEffects(a.Effects),
		})
	}
}

func joinEffects(effects []string) string {
	switch len(effects) {
	case 0:
		return ""
	case 1:
		return effects[0]
	}
	out := effects[0]
	for _, e := range effects[1:] {
		out += ", " + e
	}
	return out
}
