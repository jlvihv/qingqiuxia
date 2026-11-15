package api_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// msgReq2 builds a request for the filter tests.
func msgReq2(method, raw string) msg.Request {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	r := msg.Request{Method: method, URL: u, Proto: "HTTP/1.1"}
	r.Header = http.Header{"Accept": {"*/*"}}
	r.Body = []byte("hi!")
	return r
}

// msgRespCopy is a response the tests copy and adjust.
var msgRespCopy = msg.Response{
	Status:  200,
	Proto:   "HTTP/1.1",
	Message: msg.Message{Header: http.Header{"Content-Type": {"text/plain"}}, Body: []byte("hi!")},
}

// mustGet fetches a path and decodes it into a generic value, for the tests that
// compare sizes rather than fields.
func mustGet(t *testing.T, ts *testServer, path string) any {
	t.Helper()
	var out any
	if code := ts.do(t, "GET", path, nil, &out); code != 200 {
		t.Fatalf("GET %s = %d", path, code)
	}
	return out
}

var _ = time.Now
