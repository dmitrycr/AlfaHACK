package llm

import (
	"encoding/json"
	"strings"
)

// OutputSchema emits only definitions reachable from this agent's output.
// No vendor-specific response_format is assumed or sent.
func OutputSchema(task string) ([]byte, error) {
	name, ok := map[string]string{"tag": "LLMTagResult", "profile": "LLMProfileBrief", "insight": "LLMCard", "final": "LLMCard", "check": "LLMCheckResult", "chat": "LLMAnswer"}[task]
	if !ok {
		return nil, ErrInvalid
	}
	full := JSONSchema()["$defs"].(map[string]any)
	defs := map[string]any{}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				n := strings.TrimPrefix(ref, "#/$defs/")
				if _, seen := defs[n]; !seen {
					defs[n] = full[n]
					visit(full[n])
				}
			}
			for k, v := range x {
				if k != "$ref" {
					visit(v)
				}
			}
		case []any:
			for _, v := range x {
				visit(v)
			}
		}
	}
	root := map[string]any{"$ref": "#/$defs/" + name}
	visit(root)
	root["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	root["$defs"] = defs
	return json.Marshal(root)
}
