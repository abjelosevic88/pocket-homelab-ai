package server

import (
	"encoding/json"
	"strings"
)

// portableToolList readies a tools/list result for every agent's model.
// Google's API (Antigravity, Gemini CLI) refuses the whole request when a
// tool's schema has an empty string among an enum's values ("enum[4]:
// cannot be empty"), which some Pocket tools use for "none" (Errors'
// update_issue for = "" = for good). Those values are left out: leaving the
// field out means the same to the apps.
func portableToolList(result any) any {
	raw, err := json.Marshal(result)
	if err != nil {
		return result
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return result
	}
	tools, _ := m["tools"].([]any)
	for _, t := range tools {
		if tm, ok := t.(map[string]any); ok {
			if s, ok := tm["inputSchema"]; ok {
				tm["inputSchema"] = portableSchema(s)
			}
		}
	}
	return m
}

// portableSchema drops empty strings from every enum in a JSON schema (an
// enum left with nothing is dropped too). An enum of a field that is not a
// string (habits' status 1 / -1) is refused by Google's API too: its values
// go into the description instead.
func portableSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if typ, _ := x["type"].(string); typ != "" && typ != "string" {
			if list, ok := x["enum"].([]any); ok {
				vals := make([]string, 0, len(list))
				for _, e := range list {
					b, _ := json.Marshal(e)
					vals = append(vals, string(b))
				}
				desc, _ := x["description"].(string)
				if desc != "" {
					desc += " "
				}
				x["description"] = desc + "One of: " + strings.Join(vals, ", ") + "."
				delete(x, "enum")
			}
		}
		for k, sub := range x {
			if k == "enum" {
				if list, ok := sub.([]any); ok {
					keep := make([]any, 0, len(list))
					for _, e := range list {
						if s, isStr := e.(string); isStr && s == "" {
							continue
						}
						keep = append(keep, e)
					}
					if len(keep) == 0 {
						delete(x, k)
					} else {
						x[k] = keep
					}
				}
				continue
			}
			if k == "const" || k == "default" || k == "examples" {
				continue // values, not schemas
			}
			x[k] = portableSchema(sub)
		}
		return x
	case []any:
		for i := range x {
			x[i] = portableSchema(x[i])
		}
		return x
	}
	return v
}
