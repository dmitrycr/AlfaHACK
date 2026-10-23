package llm

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// JSONSchema exports the wire structure from the same Go types used by handlers.
// Cross-record consistency, citations and snapshot checks remain runtime checks.
func JSONSchema() map[string]any {
	defs := map[string]any{}
	var schema func(reflect.Type) map[string]any
	schema = func(t reflect.Type) map[string]any {
		if t == reflect.TypeFor[time.Time]() {
			return map[string]any{"type": "string", "format": "date-time"}
		}
		if t == reflect.TypeFor[json.RawMessage]() {
			return map[string]any{}
		}
		switch t.Kind() {
		case reflect.Pointer:
			return map[string]any{"anyOf": []any{schema(t.Elem()), map[string]any{"type": "null"}}}
		case reflect.Struct:
			name := "LLM" + t.Name()
			if _, ok := defs[name]; !ok {
				defs[name] = nil
				props := map[string]any{}
				required := []string{}
				for i := 0; i < t.NumField(); i++ {
					f := t.Field(i)
					tag := strings.Split(f.Tag.Get("json"), ",")
					if tag[0] == "-" || tag[0] == "" {
						continue
					}
					props[tag[0]] = schema(f.Type)
					if len(tag) == 1 || tag[1] != "omitempty" {
						required = append(required, tag[0])
					}
				}
				defs[name] = map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
			}
			return map[string]any{"$ref": "#/$defs/" + name}
		case reflect.Slice:
			return map[string]any{"type": "array", "items": schema(t.Elem())}
		case reflect.Map:
			return map[string]any{"type": "object", "additionalProperties": schema(t.Elem())}
		case reflect.String:
			return map[string]any{"type": "string"}
		case reflect.Int, reflect.Int32, reflect.Int64:
			return map[string]any{"type": "integer"}
		case reflect.Float32, reflect.Float64:
			return map[string]any{"type": "number"}
		case reflect.Bool:
			return map[string]any{"type": "boolean"}
		default:
			return map[string]any{}
		}
	}
	for _, t := range []reflect.Type{reflect.TypeFor[WorkflowTrace](), reflect.TypeFor[ProfileInput](), reflect.TypeFor[Prepared](), reflect.TypeFor[TagInput](), reflect.TypeFor[TagResult](), reflect.TypeFor[InsightInput](), reflect.TypeFor[CheckInput](), reflect.TypeFor[CheckResult](), reflect.TypeFor[Insight](), reflect.TypeFor[ChatInput](), reflect.TypeFor[Answer](), reflect.TypeFor[Call](), reflect.TypeFor[Completion](), reflect.TypeFor[Trace]()} {
		schema(t)
	}
	enums := map[string]map[string][]string{
		"LLMReadyEvent": {"type": {"regulation", "macro", "commodity", "corporate", "industry", "regional", "sanctions"}, "sectors": Sectors, "factors": Factors, "direction": {"positive", "negative", "unclear"}, "horizon": {"short", "medium", "long"}},
		"LLMTagResult":  {"status": {"ready", "noise", "insufficient_data"}},
		"LLMCard":       {"confidence": {"low", "medium", "high"}},
		"LLMStatement":  {"layer": {"fact", "bank", "ai"}},
		"LLMSource":     {"kind": {"news", "bank_analytics"}},
		"LLMCall":       {"task": {"tag", "profile", "insight", "final", "check", "chat"}},
	}
	for name, fields := range enums {
		props := defs[name].(map[string]any)["properties"].(map[string]any)
		for field, allowed := range fields {
			s := props[field].(map[string]any)
			if s["type"] == "array" {
				s = s["items"].(map[string]any)
			}
			s["enum"] = allowed
		}
	}
	props := func(name string) map[string]any { return defs[name].(map[string]any)["properties"].(map[string]any) }
	for _, name := range []string{"LLMTagInput", "LLMPrepared"} {
		props(name)["input_revision"].(map[string]any)["minimum"] = 1
	}
	for _, name := range []string{"LLMPrepared", "LLMInsight", "LLMMatch"} {
		props(name)["event_version"].(map[string]any)["minimum"] = 1
	}
	for _, name := range []string{"LLMInsight", "LLMMatch"} {
		props(name)["profile_version"].(map[string]any)["minimum"] = 1
	}
	props("LLMReadyEvent")["importance"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 5}
	for _, name := range []string{"LLMMatch", "LLMContribution"} {
		for _, field := range []string{"score", "weight", "match", "contribution"} {
			if p, ok := props(name)[field].(map[string]any); ok {
				p["minimum"] = 0
				p["maximum"] = 1
			}
		}
	}
	for _, name := range []string{"LLMTagInput", "LLMReadyEvent"} {
		props(name)["title"].(map[string]any)["maxLength"] = 500
	}
	props("LLMTagInput")["text"].(map[string]any)["maxLength"] = 20000
	props("LLMReadyEvent")["sources"].(map[string]any)["minItems"] = 1
	props("LLMSource")["url"] = map[string]any{"type": "string", "format": "uri", "pattern": "^https?://", "maxLength": 2048}
	for _, name := range []string{"LLMPrepared"} {
		props(name)["schema_version"].(map[string]any)["const"] = ContractVersion
		props(name)["vocabulary_version"].(map[string]any)["const"] = VocabularyVersion
	}
	variants := []any{}
	for _, task := range []string{"tag", "profile", "insight", "final", "check", "chat"} {
		name := map[string]string{"tag": "LLMTagInput", "profile": "LLMProfileInput", "insight": "LLMInsightInput", "final": "LLMInsightInput", "check": "LLMCheckInput", "chat": "LLMChatInput"}[task]
		variants = append(variants, map[string]any{"properties": map[string]any{"task": map[string]any{"const": task}, "input": map[string]any{"$ref": "#/$defs/" + name}}})
	}
	defs["LLMCall"].(map[string]any)["oneOf"] = variants
	return map[string]any{"$schema": "https://json-schema.org/draft/2020-12/schema", "$id": "urn:alfa:backend-llm:v1", "$defs": defs}
}
