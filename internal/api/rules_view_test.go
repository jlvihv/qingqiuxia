package api_test

import (
	"testing"

	"github.com/jlvihv/qingqiuxia/internal/api"
	"github.com/jlvihv/qingqiuxia/internal/rule"
)

// TestRuleReadsBackAsWritten checks that a rule can be read, changed and written
// again: a list of effects is readable but cannot be round-tripped, so a caller
// that wants to change one field would have to guess the rest.
func TestRuleReadsBackAsWritten(t *testing.T) {
	ts := newServer(t, api.Options{})
	if code := ts.do(t, "POST", "/api/rules", map[string]any{
		"name": "打码", "kind": "rewrite", "phase": "response", "pattern": "*/login",
		"actions": []map[string]any{{"type": "replace-body", "find": "real", "replace": "***"}},
	}, nil); code != 201 {
		t.Fatalf("create = %d", code)
	}

	var got rule.Spec
	if code := ts.do(t, "GET", "/api/rules/1", nil, &got); code != 200 {
		t.Fatalf("get = %d", code)
	}
	if got.Name != "打码" || got.Kind != "rewrite" || got.Phase != "response" {
		t.Errorf("read back %+v", got)
	}
	if len(got.Actions) != 1 {
		t.Fatalf("actions = %v", got.Actions)
	}
	a := got.Actions[0]
	if a.Type != "replace-body" || a.Find != "real" || a.Replace != "***" {
		t.Errorf("action = %+v", a)
	}

	// Written again with one field changed, it keeps the rest.
	got.Enabled = boolPtr(false)
	if code := ts.do(t, "PUT", "/api/rules", map[string]any{"rules": []rule.Spec{got}}, nil); code != 200 {
		t.Fatalf("write back = %d", code)
	}
	var after rule.Spec
	ts.do(t, "GET", "/api/rules/1", nil, &after)
	if after.Enabled == nil || *after.Enabled {
		t.Error("the change did not take")
	}
	if len(after.Actions) != 1 || after.Actions[0].Replace != "***" {
		t.Errorf("the rest of the rule was lost: %+v", after)
	}
}

// TestRuleAndBreakpointAreDistinct checks the two things a caller must not
// confuse: a rule acts on its own, a breakpoint waits for a decision. They are
// separate paths, and neither may appear in the other's list.
func TestRuleAndBreakpointAreDistinct(t *testing.T) {
	ts := newServer(t, api.Options{})

	ts.do(t, "POST", "/api/rules", map[string]any{
		"name": "规则", "kind": "abort", "phase": "request", "pattern": "*/x",
	}, nil)
	ts.do(t, "PUT", "/api/breakpoints", map[string]any{
		"breakpoints": []map[string]any{{"name": "断点", "phase": "request", "pattern": "*/y"}},
	}, nil)

	var rules struct {
		Rules []struct {
			Name string `json:"name"`
		} `json:"rules"`
	}
	ts.do(t, "GET", "/api/rules", nil, &rules)
	if len(rules.Rules) != 1 || rules.Rules[0].Name != "规则" {
		t.Errorf("rules = %+v, want only the rule", rules.Rules)
	}
	var bps struct {
		Breakpoints []struct {
			Name string `json:"name"`
		} `json:"breakpoints"`
	}
	ts.do(t, "GET", "/api/breakpoints", nil, &bps)
	if len(bps.Breakpoints) != 1 || bps.Breakpoints[0].Name != "断点" {
		t.Errorf("breakpoints = %+v, want only the breakpoint", bps.Breakpoints)
	}
}

func boolPtr(b bool) *bool { return &b }
