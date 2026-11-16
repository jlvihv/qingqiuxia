package proxy

import (
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// captureRequest reads a request's body into the message model, within limit.
//
// A body longer than the limit is not cut: the first limit bytes are kept for
// inspection and the rest is returned as a reader the proxy forwards untouched,
// so the origin still receives the whole body. Cutting it here sent the origin a
// different request from the one the client made, which is the one thing a
// debugging proxy must never do.
func captureRequest(r *http.Request, limit int64) (*msg.Request, io.Reader, error) {
	// The request's header map is taken over rather than copied: net/http is
	// done with it once the handler runs, and a copy of every map was the
	// second largest allocation under load.
	req := &msg.Request{
		Method:  r.Method,
		URL:     cloneURL(r.URL),
		Proto:   r.Proto,
		Message: msg.Message{Header: r.Header},
	}
	if r.Body == nil || r.Body == http.NoBody {
		return req, nil, nil
	}

	// A limit of 0 means no limit at all.
	if limit <= 0 {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, nil, err
		}
		r.Body.Close()
		req.Body = body
		return req, nil, nil
	}

	// Read the start of the body. Both a known and an unknown length take this
	// path: a chunked body has no Content-Length to compare against, and reading
	// it whole would buffer a body of any size.
	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return nil, nil, err
	}
	req.Body = body

	// One byte past the limit says whether anything is left. It is kept and
	// handed back with the rest, so the origin sees the body unchanged.
	var one [1]byte
	n, rerr := io.ReadFull(r.Body, one[:])
	if n == 0 {
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			r.Body.Close()
			return req, nil, nil
		}
		if rerr != nil {
			return nil, nil, rerr
		}
	}
	req.Truncated = true
	return req, io.MultiReader(bytesReader(one[:n]), r.Body), nil
}

// captureResponse turns an origin response into the message model, keeping the
// body within limit. A body past the limit keeps its first part as Body, and the
// unread remainder as Stream, so the client still receives every byte.
//
// It returns the response and the time to its first body byte, which is zero
// for a response without a body.
func captureResponse(raw *http.Response, limit int64, start time.Time) (*msg.Response, time.Duration, error) {
	resp := &msg.Response{
		Status:  raw.StatusCode,
		Proto:   raw.Proto,
		Message: msg.Message{Header: raw.Header},
	}
	if raw.StatusCode == http.StatusNoContent || raw.StatusCode == http.StatusNotModified {
		return resp, 0, nil
	}
	mediaType := msg.MediaType(raw.Header.Get("Content-Type"))

	// A live stream is never buffered: the client must see events as they
	// come, and a debugger that waited for the end would see none.
	if msg.IsStreaming(mediaType) {
		resp.Stream = raw.Body
		return resp, time.Since(start), nil
	}

	// The read buffer comes from a pool: a 32 KiB allocation per response was
	// most of the core's garbage under load.
	bp := bufferPool.Get().(*[]byte)
	buf := *bp
	defer bufferPool.Put(bp)

	var (
		body    []byte
		total   int64
		ttfb    time.Duration
		firstOK = false
	)
	for {
		// Stop reading once the capture limit is reached: what is left stays in
		// raw.Body, which the client reads through.
		room := int64(len(buf))
		if limit > 0 {
			left := limit - total
			if left <= 0 {
				break
			}
			if left < room {
				room = left
			}
		}
		n, err := raw.Body.Read(buf[:room])
		if n > 0 {
			if !firstOK {
				ttfb = time.Since(start)
				firstOK = true
			}
			body = append(body, buf[:n]...)
			total += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return resp, ttfb, err
		}
	}
	resp.Body = body
	if limit > 0 && total >= limit {
		// The limit may have been reached exactly at the end of the body, so ask
		// before deciding there is more to forward. The byte read is kept and
		// handed to the client, or the response would lose one.
		more, rest, err := peekMore(raw.Body)
		if err != nil {
			return resp, ttfb, err
		}
		if more {
			resp.Truncated = true
			resp.Stream = rest
		}
	}
	return resp, ttfb, nil
}

// peekMore reads at most one byte to learn whether r has more, returning a
// reader that yields that byte and then the rest of r.
func peekMore(r io.ReadCloser) (bool, io.ReadCloser, error) {
	var one [1]byte
	n, err := r.Read(one[:])
	if n > 0 {
		return true, readCloser{Reader: io.MultiReader(bytesReader(one[:n]), r), Closer: r}, nil
	}
	if err == io.EOF {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	// A zero-byte read with no error is allowed by io.Reader; the body is not
	// over, so forward it as it is.
	return true, r, nil
}

// readCloser pairs a reader with the closer that owns it, for the case where
// some bytes were already taken from that closer.
type readCloser struct {
	io.Reader
	io.Closer
}

// bytesReader reads from b without the caller having to keep a slice header.
func bytesReader(b []byte) io.Reader { return &sliceReader{b: b} }

type sliceReader struct {
	b []byte
	i int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

// cloneURL copies a URL.
func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	c := *u
	return &c
}

// bufferPool recycles the buffers the capture reads bodies through. A proxy
// reads a body per response, so allocating one each time was the largest
// source of garbage the profiler showed.
var bufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32<<10)
		return &b
	},
}
