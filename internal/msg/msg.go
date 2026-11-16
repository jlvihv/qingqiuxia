// Package msg is the traffic model shared by the whole core: an HTTP request
// and response as the proxy sees them, independent of net/http's wire types.
//
// The other packages depend on these types rather than on *http.Request, so
// that rules, capture and the control API never have to know how a message
// arrived (plain proxy, intercepted TLS, HTTP/2, replay).
package msg

import (
	"net/http"
	"net/url"
)

// Message is what requests and responses have in common: headers and a body.
type Message struct {
	// Header holds the message headers, with the original key casing lost as
	// in net/http. Hop-by-hop headers are not part of it.
	//
	// The proxy takes ownership of the header map net/http parsed rather than
	// copying it: the copy showed up as the second largest allocation under
	// load. A message handed to the session store is cloned there, so a
	// snapshot never aliases a header the connection still owns.
	Header http.Header

	// Body holds the body bytes as they went on the wire, still encoded as
	// the Content-Encoding header says. Rules decode and re-encode it around
	// their edits, so what the client receives stays encoded the same way.
	//
	// A snapshot copies this, and the copy costs the body's size: the core's
	// largest allocation under load. It is copied because the proxy keeps editing
	// the message it captured, and a snapshot must not move under a reader.
	Body []byte

	// Stream holds the rest of a body that was not buffered: a live stream such
	// as server-sent events, or the part of a long body past the capture limit.
	// When Body is also set, Body is the start that was kept for inspection and
	// Stream carries what still has to reach the client, so the two together are
	// the whole body.
	Stream ReadCloser

	// Truncated reports that Body is only the start of a longer body, because
	// the capture limit was reached.
	Truncated bool
}

// ReadCloser is io.ReadCloser, named here to keep the import list short.
type ReadCloser = interface {
	Read([]byte) (int, error)
	Close() error
}

// ContentType is the message's media type, without its parameters.
func (m *Message) ContentType() string {
	return MediaType(m.Header.Get("Content-Type"))
}

// Buffered reports whether the body is held in memory and may be inspected.
func (m *Message) Buffered() bool { return m.Stream == nil }

// SetBody replaces the body with b, dropping any stream and truncation and
// keeping Content-Length in step.
func (m *Message) SetBody(b []byte) {
	m.Body = b
	m.Stream = nil
	m.Truncated = false
	m.SetHeader("Content-Length", itoa(len(b)))
}

// ClearBody drops the body, as a HEAD response or a mock without one.
func (m *Message) ClearBody() {
	m.Body = nil
	m.Stream = nil
	m.Truncated = false
	m.Header.Del("Content-Length")
}

// SetHeader sets one header value.
func (m *Message) SetHeader(name, value string) {
	if m.Header == nil {
		m.Header = make(http.Header, 8)
	}
	m.Header.Set(name, value)
}

// Close releases a stream the message still holds.
func (m *Message) Close() {
	if m.Stream != nil {
		m.Stream.Close()
		m.Stream = nil
	}
}

// cloneMessage copies the parts a snapshot must own.
func (m *Message) cloneMessage() Message {
	c := Message{Truncated: m.Truncated}
	if m.Header != nil {
		c.Header = m.Header.Clone()
	}
	if m.Body != nil {
		c.Body = append([]byte(nil), m.Body...)
	}
	// A clone never keeps the stream: a snapshot is what was captured, and the
	// stream is the live connection's, which the proxy still owns.
	return c
}

// Request is one HTTP request.
type Request struct {
	Message
	Method string
	URL    *url.URL
	Proto  string
}

// Clone returns a deep copy that a snapshot may keep.
func (r *Request) Clone() *Request {
	if r == nil {
		return nil
	}
	c := &Request{Message: r.Message.cloneMessage(), Method: r.Method, Proto: r.Proto}
	if r.URL != nil {
		u := *r.URL
		c.URL = &u
	}
	return c
}

// Response is one HTTP response.
type Response struct {
	Message
	Status int
	// Reason is the reason phrase that goes with Status, empty for the
	// standard one.
	Reason string
	Proto  string
}

// Clone returns a deep copy that a snapshot may keep.
func (r *Response) Clone() *Response {
	if r == nil {
		return nil
	}
	return &Response{Message: r.Message.cloneMessage(), Status: r.Status, Reason: r.Reason, Proto: r.Proto}
}

func itoa(n int) string {
	// strconv.Itoa without the import churn on the hot path's neighbours.
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
