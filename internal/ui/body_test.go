package ui_test

import (
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// sendThrough starts the core's proxy, sends one request through it, and
// returns the backend, the row it recorded and the origin it hit.
func sendThrough(t *testing.T, handler http.HandlerFunc, send func(string) *http.Request) (*qqx.Local, qqx.Session) {
	t.Helper()
	local, _, _, pr := newLocal(t)
	addr, err := local.StartProxy("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { pr.Stop() })

	origin := httptest.NewServer(handler)
	t.Cleanup(origin.Close)

	pu, _ := url.Parse("http://" + addr)
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(pu)},
		// A redirect is part of what is being checked, not something to follow.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(send(origin.URL))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()

	all, _, _, err := local.Sessions(context.Background(), -1)
	if err != nil || len(all) != 1 {
		t.Fatalf("sessions = %d, %v", len(all), err)
	}
	return local, all[0]
}

// TestDetailShowsRequestBody checks that the request body a client sent comes
// back for the panel, with its headers.
func TestDetailShowsRequestBody(t *testing.T) {
	local, sess := sendThrough(t,
		func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) },
		func(u string) *http.Request {
			req, _ := http.NewRequest("POST", u+"/api/login", strings.NewReader(`{"user":"ada"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Trace", "abc")
			return req
		})

	d, err := local.Detail(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Request.Body != `{"user":"ada"}` {
		t.Errorf("request body = %q", d.Request.Body)
	}
	if d.Request.ContentType != "application/json" {
		t.Errorf("content type = %q", d.Request.ContentType)
	}
	var found bool
	for _, h := range d.Request.Headers {
		if h.Name == "X-Trace" && h.Value == "abc" {
			found = true
		}
	}
	if !found {
		t.Errorf("the request header is missing: %+v", d.Request.Headers)
	}
}

// TestDetailDecodesGzippedResponse checks that a compressed body is shown
// decoded: what a reader wants from a gzipped JSON is the JSON.
func TestDetailDecodesGzippedResponse(t *testing.T) {
	payload := `{"items":[1,2,3],"note":"压缩过的"}`
	local, sess := sendThrough(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			zw.Write([]byte(payload))
			zw.Close()
		},
		func(u string) *http.Request {
			req, _ := http.NewRequest("GET", u+"/json", nil)
			// Ask for it compressed, as a browser would.
			req.Header.Set("Accept-Encoding", "gzip")
			return req
		})

	d, err := local.Detail(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.Response.Body, "压缩过的") {
		t.Fatalf("response body = %q, want the decoded JSON", d.Response.Body)
	}
	if d.Response.Binary {
		t.Error("a JSON body was called binary")
	}
}

// TestDetailMarksBinary checks that an image is reported as binary with its
// size rather than shown as noise.
func TestDetailMarksBinary(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0xff, 0xfe}
	local, sess := sendThrough(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			w.Write(png)
		},
		func(u string) *http.Request {
			req, _ := http.NewRequest("GET", u+"/logo.png", nil)
			return req
		})

	d, err := local.Detail(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Response.Binary {
		t.Error("an image was not reported as binary")
	}
	if d.Response.Size != int64(len(png)) {
		t.Errorf("size = %d, want %d", d.Response.Size, len(png))
	}
	if d.Response.Body != "" {
		t.Errorf("a binary body was rendered as text: %q", d.Response.Body)
	}
}

// TestDetailOfUnknownSession checks that asking for a session the core dropped
// is an error the panel can show, not an empty pane.
func TestDetailOfUnknownSession(t *testing.T) {
	local, _, _, _ := newLocal(t)
	if _, err := local.Detail(context.Background(), 999); err == nil {
		t.Error("no error for a session that is not there")
	}
}

// TestBodyPaneDrawsTheBody drives the panel: choosing a row and its response tab
// must put the body on screen.
func TestBodyPaneDrawsTheBody(t *testing.T) {
	local, sess := sendThrough(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("the-body-of-the-response"))
		},
		func(u string) *http.Request {
			req, _ := http.NewRequest("GET", u+"/text", nil)
			return req
		})

	store := qqx.NewStore(10)
	store.Upsert(sess)
	app := qqx.NewApp(store)
	app.SetBackend(local, local)
	app.Selected = 0
	app.Detail = 2 // 响应
	tt := ui.NewTester(app.View, 1200, 800)
	tt.Frame()

	// The body itself is drawn in a read-only text area, whose content the
	// tester does not report as text; what the pane does report is the line it
	// states above the body, which is what tells a reader what they are looking
	// at.
	if !tt.HasText("响应体") {
		t.Errorf("the response body heading is not on screen: %v", tt.Texts())
	}
	if !tt.HasText("text/plain · 1 行 · 24") {
		t.Errorf("the body's size line is not on screen: %v", tt.Texts())
	}
	// And the text the pane was given is the body the origin sent.
	if d, id := app.Bodies(); id != sess.ID || len(d.Response.Body) != 24 {
		t.Errorf("the pane holds %q for session %d", d.Response.Body, id)
	}
}
