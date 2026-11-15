package ui

import (
	"context"
	"time"
)

// Backend is what the window needs from the core.
//
// It is an interface because the window is one client among others: the desktop
// window calls it in-process, and a script or a browser reaches the same shape
// over the API. Sharing the interface is what lets the two exist without the
// window linking the proxy: this package names six operations, and knows none of
// them by their implementation.
//
// Every method must be safe to call from any goroutine: the window calls them
// from its own, while the core serves traffic from others.
type Backend interface {
	// Status describes the core: where its proxy listens, what it is doing.
	Status(ctx context.Context) (Status, error)
	// Sessions returns the traffic, newest first, and how many the core holds.
	// A negative limit asks for all of them.
	//
	// The rows carry no bodies: a capture of thousands with their bodies in
	// memory is most of a gigabyte for detail one click asks for.
	Sessions(ctx context.Context, limit int) ([]Session, int, uint64, error)
	// Detail returns one session's request and response bodies and headers,
	// which Sessions deliberately left out.
	Detail(ctx context.Context, id uint64) (Detail, error)
	// SetCapture turns recording on or off.
	SetCapture(ctx context.Context, on bool) error
	// Pauses lists what is waiting at a breakpoint.
	Pauses(ctx context.Context) ([]Pause, error)
	// Resume releases a paused message, or refuses it.
	Resume(ctx context.Context, pauseID uint64, abort bool) error
	// Watch calls fn for every change until ctx is cancelled, and blocks: the
	// window runs it in a goroutine. This is what replaces a network stream,
	// and it carries whole values rather than JSON to encode and decode.
	Watch(ctx context.Context, fn func(Change)) error
}

// Detail is what one session holds in full: the parts a list row leaves out.
type Detail struct {
	Request  Message
	Response Message
}

// Change is one thing that happened in the core.
type Change struct {
	// Session is the session that changed, set for ChangeSession.
	Session *Session
	// Pause is the message that stopped or resumed, set for the pause kinds.
	Pause *Pause
	// Kind says which of the above is set.
	Kind ChangeKind
}

// ChangeKind is what a Change reports.
type ChangeKind uint8

const (
	// ChangeSession is a session that started or finished.
	ChangeSession ChangeKind = iota
	// ChangeDropped is a session the core dropped to stay within its limit.
	ChangeDropped
	// ChangePaused is a message that stopped at a breakpoint.
	ChangePaused
	// ChangeResumed is a message that left one.
	ChangeResumed
)

// Control is what the window asks the core to do to the proxy itself: start it
// on a port, stop it, move it. This is not part of Backend because a remote
// client has no business starting a proxy in another process; the window has,
// since it started the core.
type Control interface {
	// ProxyStatus says whether the proxy is running and where.
	ProxyStatus() (running bool, addr string)
	// StartProxy listens on addr and begins serving. addr may be a port, an
	// address or empty for any free port. It returns where it listens.
	StartProxy(addr string) (string, error)
	// StopProxy stops serving and releases the port.
	StopProxy() error
}

var _ = time.Second
