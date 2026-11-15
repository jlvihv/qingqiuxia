package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jlvihv/qingqiuxia/internal/api"
)

// TestEveryListedEndpointExists checks the thing a list can get wrong: naming a
// route that is not there. A caller that trusts the list and finds a 404 has no
// way to tell a missing feature from a missing resource.
func TestEveryListedEndpointExists(t *testing.T) {
	ts := newServer(t, api.Options{})

	var list struct {
		Endpoints []struct {
			Method  string `json:"method"`
			Path    string `json:"path"`
			Summary string `json:"summary"`
		} `json:"endpoints"`
	}
	if code := ts.do(t, "GET", "/api/endpoints", nil, &list); code != 200 {
		t.Fatalf("endpoints = %d", code)
	}
	if len(list.Endpoints) < 20 {
		t.Fatalf("only %d endpoints listed", len(list.Endpoints))
	}

	for _, ep := range list.Endpoints {
		// The event stream never ends: it is a route, but calling it would
		// hang the test the way it hangs anything that waits for it to finish.
		if ep.Path == "/api/events" {
			continue
		}
		// A resource id that does not exist is a 404 too, so only "route not
		// found" counts as missing: it comes back as the mux's own 404 text.
		path := strings.NewReplacer("{id}", "999999").Replace(ep.Path)
		req, err := http.NewRequest(ep.Method, ts.URL+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("%s %s: %v", ep.Method, path, err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", ep.Method, path, err)
		}
		body := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && strings.Contains(body, "page not found") {
			t.Errorf("%s %s is listed but not routed", ep.Method, ep.Path)
		}
		if ep.Summary == "" {
			t.Errorf("%s %s has no summary", ep.Method, ep.Path)
		}
	}
}

// TestEndpointsCarryParams checks that a caller learns the parameters without
// reading the prose: a list of paths and methods alone still makes it guess.
func TestEndpointsCarryParams(t *testing.T) {
	ts := newServer(t, api.Options{})

	var list struct {
		Endpoints []struct {
			Method  string `json:"method"`
			Path    string `json:"path"`
			Example string `json:"example"`
			Params  []struct {
				Name   string `json:"name"`
				Values string `json:"values"`
				Help   string `json:"help"`
			} `json:"params"`
		} `json:"endpoints"`
	}
	ts.do(t, "GET", "/api/endpoints", nil, &list)

	var found bool
	for _, ep := range list.Endpoints {
		// The list holds several routes on /api/sessions; the one with
		// parameters is the GET, and its example is what a caller pastes.
		if ep.Path != "/api/sessions" || ep.Method != "GET" {
			continue
		}
		found = true
		names := map[string]bool{}
		for _, p := range ep.Params {
			names[p.Name] = true
			if p.Help == "" {
				t.Errorf("param %s has no help", p.Name)
			}
		}
		for _, want := range []string{"limit", "status", "since", "shape", "fields"} {
			if !names[want] {
				t.Errorf("the list does not mention %s", want)
			}
		}
		if !strings.Contains(ep.Example, "/api/sessions") {
			t.Errorf("example = %q", ep.Example)
		}
	}
	if !found {
		t.Fatal("the traffic list is not in the list")
	}
}

// TestEndpointsGroupFilter checks that a caller can ask for one part.
func TestEndpointsGroupFilter(t *testing.T) {
	ts := newServer(t, api.Options{})
	var list struct {
		Endpoints []struct {
			Group string `json:"group"`
		} `json:"endpoints"`
	}
	ts.do(t, "GET", "/api/endpoints?group=规则", nil, &list)
	if len(list.Endpoints) == 0 {
		t.Fatal("no rules endpoints")
	}
	for _, ep := range list.Endpoints {
		if ep.Group != "规则" {
			t.Errorf("group filter let through %q", ep.Group)
		}
	}
}

// TestEndpointsText checks the form a person reads.
func TestEndpointsText(t *testing.T) {
	ts := newServer(t, api.Options{})
	resp, err := http.Get(ts.URL + "/api/endpoints?format=text")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content type = %q", ct)
	}
	body := readAll(t, resp)
	for _, want := range []string{"== 流量 ==", "GET", "/api/sessions", "limit"} {
		if !strings.Contains(body, want) {
			t.Errorf("the text list does not mention %q", want)
		}
	}
}

// readAll reads a response body.
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}
