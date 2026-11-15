package rule

import (
	"encoding/json"
	"fmt"
)

// ParseSpecs reads a rules file: either a JSON array of specs or an object with
// a "rules" array, so that a file the GUI wrote and one a person wrote both
// load.
func ParseSpecs(data []byte) ([]Spec, error) {
	var wrapped struct {
		Rules []Spec `json:"rules"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Rules != nil {
		return wrapped.Rules, nil
	}
	var list []Spec
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("rule: parse rules: %w", err)
	}
	return list, nil
}

// MarshalSpecs writes specs as a {"rules": [...]} object.
func MarshalSpecs(specs []Spec) ([]byte, error) {
	return json.MarshalIndent(struct {
		Rules []Spec `json:"rules"`
	}{Rules: specs}, "", "  ")
}
