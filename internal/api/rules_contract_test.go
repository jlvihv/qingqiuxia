package api_test

import (
	"net/http"
	"testing"

	"github.com/jlvihv/qingqiuxia/internal/api"
)

// The rule endpoints promise a read-modify-write round trip: GET a rule, change
// one field, PUT it back. These tests hold that promise, because each of these
// broke it in a different way and all three were silent.

// TestRuleRoundTripKeepsEveryField checks that what GET returns can be sent
// straight back. GET includes "effects", which the decoder rejected as an
// unknown field, so the documented round trip answered 400.
func TestRuleRoundTripKeepsEveryField(t *testing.T) {
	ts := newServer(t, api.Options{})
	created := map[string]any{
		"name":    "restricted",
		"kind":    "mock",
		"phase":   "request",
		"pattern": "*/login",
		"methods": []string{"POST"},
		"mock":    map[string]any{"status": 200, "body": "ok"},
	}
	if code := ts.do(t, "POST", "/api/rules", created, nil); code != http.StatusCreated {
		t.Fatalf("create = %d", code)
	}

	var list struct {
		Rules []map[string]any `json:"rules"`
	}
	if code := ts.do(t, "GET", "/api/rules", nil, &list); code != 200 {
		t.Fatalf("list = %d", code)
	}
	if code := ts.do(t, "PUT", "/api/rules", map[string]any{"rules": list.Rules}, nil); code != 200 {
		t.Fatalf("writing back what GET returned answered %d", code)
	}

	// Methods survived: without it the rule matched every method after one
	// round trip.
	got := ts.rules.Rules()
	if len(got) != 1 {
		t.Fatalf("%d rules", len(got))
	}
	methods := got[0].Matcher.MethodSet()
	if len(methods) != 1 || methods[0] != "POST" {
		t.Errorf("methods = %v, want [POST]", methods)
	}
}

// TestReorderIsHonoured checks PUT as the way to reorder rules. The order used to
// be rebuilt from ID, so the submitted order was discarded and the old one came
// back — a reorder that answered 200 and did nothing.
func TestReorderIsHonoured(t *testing.T) {
	ts := newServer(t, api.Options{})
	for _, name := range []string{"first", "second"} {
		spec := map[string]any{
			"name": name, "kind": "mock", "phase": "request",
			"pattern": "*/" + name, "mock": map[string]any{"status": 200},
		}
		if code := ts.do(t, "POST", "/api/rules", spec, nil); code != http.StatusCreated {
			t.Fatalf("create %s = %d", name, code)
		}
	}
	var list struct {
		Rules []map[string]any `json:"rules"`
	}
	if code := ts.do(t, "GET", "/api/rules", nil, &list); code != 200 {
		t.Fatalf("list = %d", code)
	}
	reversed := []map[string]any{list.Rules[1], list.Rules[0]}
	if code := ts.do(t, "PUT", "/api/rules", map[string]any{"rules": reversed}, nil); code != 200 {
		t.Fatalf("PUT = %d", code)
	}
	got := ts.rules.Rules()
	if len(got) != 2 || got[0].Name != "second" || got[1].Name != "first" {
		t.Errorf("order = %s,%s; want second,first", got[0].Name, got[1].Name)
	}
}

// TestPriorityStillOutranksListPosition checks that honouring the submitted order
// did not swallow Priority, which a rule sets on itself.
func TestPriorityStillOutranksListPosition(t *testing.T) {
	ts := newServer(t, api.Options{})
	for _, spec := range []map[string]any{
		{"name": "urgent", "kind": "mock", "phase": "request", "pattern": "*/u",
			"priority": 10, "mock": map[string]any{"status": 200}},
		{"name": "ordinary", "kind": "mock", "phase": "request", "pattern": "*/o",
			"mock": map[string]any{"status": 200}},
	} {
		if code := ts.do(t, "POST", "/api/rules", spec, nil); code != http.StatusCreated {
			t.Fatalf("create = %d", code)
		}
	}
	got := ts.rules.Rules()
	if len(got) != 2 || got[0].Name != "urgent" {
		t.Errorf("priority did not order the rules: %s first", got[0].Name)
	}
}
