package api_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/api"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// seedTraffic fills the store with a spread of sessions to filter.
func (ts *testServer) seedTraffic(t *testing.T) {
	t.Helper()
	now := time.Now()
	specs := []struct {
		method, url string
		status      int
		age         time.Duration
	}{
		{"GET", "https://api.test/v1/users", 200, 0},
		{"GET", "https://api.test/v1/orders", 500, 10 * time.Second},
		{"POST", "https://api.test/v1/login", 201, 20 * time.Second},
		{"GET", "https://cdn.test/app.js", 404, 30 * time.Second},
		{"GET", "https://api.test/v1/slow", 502, 5 * time.Minute},
	}
	for _, s := range specs {
		req := msgReq2(s.method, s.url)
		sess := ts.sessions.Begin(&req, now.Add(-s.age))
		resp := msgRespCopy
		resp.Status = s.status
		sess.Complete(&resp, nil, now.Add(-s.age))
	}
}

func TestFilterStatusFamily(t *testing.T) {
	ts := newServer(t, api.Options{})
	ts.seedTraffic(t)

	var body struct {
		Rows [][]any `json:"rows"`
		Cols []string
	}
	if code := ts.do(t, "GET", "/api/sessions?status=5xx&shape=table", nil, &body); code != 200 {
		t.Fatalf("code = %d", code)
	}
	if len(body.Rows) != 2 {
		t.Fatalf("5xx rows = %d, want 2: %v", len(body.Rows), body.Rows)
	}
}

func TestFilterSince(t *testing.T) {
	ts := newServer(t, api.Options{})
	ts.seedTraffic(t)

	var body struct {
		Total int `json:"total"`
	}
	ts.do(t, "GET", "/api/sessions?since=1m", nil, &body)
	if body.Total != 4 {
		t.Errorf("within a minute = %d, want 4", body.Total)
	}
	ts.do(t, "GET", "/api/sessions?since=10m", nil, &body)
	if body.Total != 5 {
		t.Errorf("within ten minutes = %d, want 5", body.Total)
	}
}

func TestFilterHostAndMethod(t *testing.T) {
	ts := newServer(t, api.Options{})
	ts.seedTraffic(t)

	var body struct {
		Total int `json:"total"`
	}
	ts.do(t, "GET", "/api/sessions?host=cdn.test", nil, &body)
	if body.Total != 1 {
		t.Errorf("cdn.test = %d rows, want 1", body.Total)
	}
	ts.do(t, "GET", "/api/sessions?method=POST", nil, &body)
	if body.Total != 1 {
		t.Errorf("POST = %d rows, want 1", body.Total)
	}
	// A plain string is a substring test, so a path finds its requests without
	// the caller spelling out wildcards.
	ts.do(t, "GET", "/api/sessions?url=/v1/", nil, &body)
	if body.Total != 4 {
		t.Errorf("url=/v1/ = %d rows, want 4", body.Total)
	}
	// Combined filters are AND.
	ts.do(t, "GET", "/api/sessions?host=api.test&status=5xx", nil, &body)
	if body.Total != 2 {
		t.Errorf("api.test 5xx = %d rows, want 2", body.Total)
	}
}

func TestTableShapeIsSmaller(t *testing.T) {
	ts := newServer(t, api.Options{})
	ts.seedTraffic(t)

	plain, _ := json.Marshal(mustGet(t, ts, "/api/sessions?limit=5"))
	table, _ := json.Marshal(mustGet(t, ts, "/api/sessions?limit=5&shape=table"))
	t.Logf("objects: %d bytes, table: %d bytes", len(plain), len(table))
	if len(table) >= len(plain) {
		t.Errorf("table shape is not smaller: %d vs %d", len(table), len(plain))
	}
}

func TestFieldsNarrowTheTable(t *testing.T) {
	ts := newServer(t, api.Options{})
	ts.seedTraffic(t)

	var body struct {
		Cols []string `json:"cols"`
	}
	ts.do(t, "GET", "/api/sessions?shape=table&fields=id,status", nil, &body)
	if len(body.Cols) != 2 || body.Cols[0] != "id" || body.Cols[1] != "status" {
		t.Errorf("cols = %v", body.Cols)
	}
}

func TestDetailPart(t *testing.T) {
	ts := newServer(t, api.Options{})
	req := msgReq2("GET", "https://api.test/v1/x")
	sess := ts.sessions.Begin(&req, time.Now())
	sess.Complete(&msgRespCopy, nil, time.Now())

	summary, _ := json.Marshal(mustGet(t, ts, fmt.Sprintf("/api/sessions/%d?part=summary", sess.ID)))
	all, _ := json.Marshal(mustGet(t, ts, fmt.Sprintf("/api/sessions/%d", sess.ID)))
	t.Logf("summary %d bytes, all %d bytes", len(summary), len(all))
	if len(summary) >= len(all) {
		t.Errorf("summary is not smaller than the whole: %d vs %d", len(summary), len(all))
	}

	var full struct {
		Request struct {
			Body string `json:"body"`
		} `json:"request"`
	}
	ts.do(t, "GET", fmt.Sprintf("/api/sessions/%d?part=body&max=3", sess.ID), nil, &full)
	if full.Request.Body != "hi!" {
		t.Errorf("truncated body = %q, want 3 bytes of it", full.Request.Body)
	}
}

func TestBadFilterIsReported(t *testing.T) {
	ts := newServer(t, api.Options{})
	var errResp struct {
		Error string `json:"error"`
	}
	if code := ts.do(t, "GET", "/api/sessions?since=banana", nil, &errResp); code != 400 {
		t.Errorf("code = %d, want 400", code)
	}
	if errResp.Error == "" {
		t.Error("no reason given")
	}
	if code := ts.do(t, "GET", "/api/sessions/1?part=nonsense", nil, &errResp); code != 400 {
		t.Errorf("bad part: code = %d, want 400", code)
	}
}

var _ = session.Pending
