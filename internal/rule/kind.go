// Package rule is the matching and rewriting engine: which requests a rule
// applies to, and what it does to them.
//
// A rule names a URL pattern and a phase (request or response), and carries a
// list of actions. Matching is compiled once, so the proxy's hot path is a
// handful of comparisons rather than a regexp per rule per byte.
package rule

import (
	"fmt"
	"strings"
)

// Phase says which side of an exchange a rule acts on.
type Phase uint8

const (
	// OnRequest acts before the request goes to the origin, or instead of it.
	OnRequest Phase = iota
	// OnResponse acts on the response on its way back to the client.
	OnResponse
)

// String names the phase for the API and the rules file.
func (p Phase) String() string {
	switch p {
	case OnRequest:
		return "request"
	case OnResponse:
		return "response"
	}
	return "unknown"
}

// ParsePhase reads a phase name.
func ParsePhase(s string) (Phase, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "request", "req", "onrequest":
		return OnRequest, nil
	case "response", "res", "onresponse":
		return OnResponse, nil
	}
	return 0, fmt.Errorf("rule: unknown phase %q", s)
}

// Kind is what a rule does.
type Kind uint8

const (
	// Rewrite changes parts of a request or response and forwards it.
	Rewrite Kind = iota
	// Mock answers a request without going to the origin.
	Mock
	// Abort refuses a request without a response body.
	Abort
)

// String names the kind for the API.
func (k Kind) String() string {
	switch k {
	case Rewrite:
		return "rewrite"
	case Mock:
		return "mock"
	case Abort:
		return "abort"
	}
	return "unknown"
}

// ParseKind reads a kind name.
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "rewrite", "":
		return Rewrite, nil
	case "mock":
		return Mock, nil
	case "abort":
		return Abort, nil
	}
	return 0, fmt.Errorf("rule: unknown kind %q", s)
}
