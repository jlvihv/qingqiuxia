package proxy

import (
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// captureRequest reads a request's body into the message model, within limit.
// It returns the request and, when the body ran past the limit, a reader for
// the rest of it, which the proxy forwards untouched.
func captureRequest(r *http.Request, limit int64) (*msg.Request, io.Reader, error) {
	req := &msg.Request{
		Method:  r.Method,
		URL:     cloneURL(r.URL),
		Proto:   r.Proto,
		Message: msg.Message{Header: r.Header.Clone()},
	}
	if r.Body == nil || r.Body == http.NoBody {
		return req, nil, nil
	}

	// A body that announces more than the limit is not buffered at all: it
	// streams through, and the session keeps its first limit bytes.
	if limit > 0 && r.ContentLength > limit {
		head := make([]byte, limit)
		n, err := io.ReadFull(r.Body, head)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return nil, nil, err
		}
		req.Body = head[:n]
		req.Truncated = true
		ex := r.Body
		return req, ex, nil
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return nil, nil, err
	}
	r.Body.Close()
	req.Body = body
	return req, nil, nil
}

// captureResponse turns an origin response into the message model, keeping the
// body within limit. A body past the limit is kept in part and marked
// truncated, and the rest is drained so the connection can be reused.
func captureResponse(raw *http.Response, limit int64, start time.Time) (*msg.Response, *Exchange, error) {
	resp := &msg.Response{
		Status:  raw.StatusCode,
		Proto:   raw.Proto,
		Message: msg.Message{Header: raw.Header.Clone()},
	}
	if raw.StatusCode == http.StatusNoContent || raw.StatusCode == http.StatusNotModified {
		return resp, nil, nil
	}
	mediaType := msg.MediaType(raw.Header.Get("Content-Type"))

	// A live stream is never buffered: the client must see events as they
	// come, and a debugger that waited for the end would see none.
	if msg.IsStreaming(mediaType) {
		resp.Stream = raw.Body
		return resp, nil, nil
	}

	var (
		body    []byte
		total   int64
		ttfb    time.Duration
		buf     = make([]byte, 32<<10)
		firstOK = false
	)
	for {
		n, err := raw.Body.Read(buf)
		if n > 0 {
			if !firstOK {
				ttfb = time.Since(start)
				firstOK = true
			}
			if limit <= 0 || total < limit {
				keep := int64(n)
				if limit > 0 && total+keep > limit {
					keep = limit - total
				}
				body = append(body, buf[:keep]...)
			}
			total += int64(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return resp, nil, err
		}
	}
	resp.Body = body
	resp.Truncated = limit > 0 && total > limit
	return resp, &Exchange{ttfb: ttfb}, nil
}

// cloneURL copies a URL.
func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	c := *u
	return &c
}
