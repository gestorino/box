package agent

import (
	"encoding/json"

	agentv1 "concierge/engine/internal/gen/agent/v1"
)

// ActionSchema is the JSON Schema of the one action a step answers with. It is strict (every
// property required, nothing extra, no free-form object) so both Claude's and OpenAI's structured
// outputs accept it; the tool's arguments travel as a JSON object in a string.
func ActionSchema(tools []*agentv1.Tool) []byte {
	names := make([]string, 0, len(tools)+1)
	for _, t := range tools {
		names = append(names, t.Name)
	}
	names = append(names, "")
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"action", "tool", "arguments_json", "text"},
		"properties": map[string]any{
			"action":         map[string]any{"type": "string", "enum": []string{"tool_call", "say", "end"}},
			"tool":           map[string]any{"type": "string", "enum": names},
			"arguments_json": map[string]any{"type": "string"},
			"text":           map[string]any{"type": "string"},
		},
	}
	b, _ := json.Marshal(schema)
	return b
}
