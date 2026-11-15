package events

import "sync/atomic"

// Counter hands out increasing IDs for sessions and rules, starting at 1 like
// a capture tool's row numbers.
type Counter struct{ n atomic.Uint64 }

// Next returns the next ID.
func (c *Counter) Next() uint64 { return c.n.Add(1) }

// Peek returns the last ID handed out, 0 before the first.
func (c *Counter) Peek() uint64 { return c.n.Load() }
