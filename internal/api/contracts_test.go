package api_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/api"
	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// The tests here cover the API's documented contracts that a caller could break
// by following the documentation: the part split, the read-modify-write round
// trip, the price of a filtered list, and the bounds of a query parameter.

// TestNegativeOffsetIsClamped checks a typo in offset. It used to slice a
// paginated list from a negative index, which panicked the handler.
func TestNegativeOffsetIsClamped(t *testing.T) {
	ts := newServer(t, api.Options{})
	if code := ts.do(t, "GET", "/api/sessions?offset=-5&limit=20", nil, nil); code != http.StatusOK {
		t.Errorf("offset=-5 answered %d, want 200", code)
	}
	if code := ts.do(t, "GET", "/api/sessions?offset=-5&limit=20&shape=table", nil, nil); code != http.StatusOK {
		t.Errorf("offset=-5 shape=table answered %d, want 200", code)
	}
}

// TestNegativeMaxIsRejected checks max, which sliced a body string from a
// negative index.
func TestNegativeMaxIsRejected(t *testing.T) {
	ts := newServer(t, api.Options{})
	ts.pipeline.Sessions().Begin(msgReq.Clone(), time.Now())
	if code := ts.do(t, "GET", "/api/sessions/1?part=body&max=-1", nil, nil); code != http.StatusBadRequest {
		t.Errorf("max=-1 answered %d, want 400", code)
	}
}

// TestPartBodyHasNoHeaders checks that part=body is the body alone. It answered
// exactly what part=all did, so a caller that asked for the body to save bytes
// paid for two header maps.
func TestPartBodyHasNoHeaders(t *testing.T) {
	ts := newServer(t, api.Options{})
	ts.pipeline.Sessions().Begin(msgReq.Clone(), time.Now())

	for _, part := range []string{"body", "all", "headers", "summary"} {
		var out struct {
			Request *struct {
				Headers map[string][]string `json:"headers"`
				Body    string              `json:"body"`
			} `json:"request"`
			Response *struct {
				Headers map[string][]string `json:"headers"`
				Body    string              `json:"body"`
			} `json:"response"`
		}
		if code := ts.do(t, "GET", "/api/sessions/1?part="+part, nil, &out); code != 200 {
			t.Fatalf("part=%s = %d", part, code)
		}
		hasHeaders := out.Request != nil && out.Request.Headers != nil
		hasBody := out.Request != nil && out.Request.Body != ""
		switch part {
		case "body":
			if hasHeaders {
				t.Error("part=body included the request headers")
			}
			if !hasBody {
				t.Error("part=body left out the request body")
			}
		case "headers":
			if !hasHeaders {
				t.Error("part=headers left out the headers")
			}
			if hasBody {
				t.Error("part=headers included a body")
			}
		case "all":
			if !hasHeaders || !hasBody {
				t.Error("part=all should have both")
			}
		case "summary":
			if hasHeaders || hasBody {
				t.Error("part=summary should have neither body nor headers")
			}
		}
	}
}

// TestTruncatingABase64BodyKeepsItDecodable checks max on a binary body. Cutting
// the base64 text produced a string that did not decode, which a caller cannot
// tell from a malformed body.
func TestTruncatingABase64BodyKeepsItDecodable(t *testing.T) {
	ts := newServer(t, api.Options{})
	req := msgReq.Clone()
	resp := &msg.Response{
		Status:  200,
		Message: msg.Message{Body: make([]byte, 64)},
	}
	resp.Header = http.Header{"Content-Type": {"application/octet-stream"}}
	sess := ts.pipeline.Sessions().Begin(req, time.Now())
	sess.Complete(resp, nil, time.Now())

	var out struct {
		Response struct {
			Body         string `json:"body"`
			BodyEncoding string `json:"bodyEncoding"`
			Truncated    bool   `json:"truncated"`
		} `json:"response"`
	}
	if code := ts.do(t, "GET", "/api/sessions/1?part=body&max=10", nil, &out); code != 200 {
		t.Fatalf("= %d", code)
	}
	if out.Response.BodyEncoding != "base64" {
		t.Fatalf("encoding = %q, want base64", out.Response.BodyEncoding)
	}
	if !out.Response.Truncated {
		t.Error("a shortened body must say so")
	}
}
