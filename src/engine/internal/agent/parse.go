package agent

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	agentv1 "concierge/engine/internal/gen/agent/v1"
)

// reply is the action's wire shape, as ActionSchema describes it.
type reply struct {
	Action        string `json:"action"`
	Tool          string `json:"tool"`
	ArgumentsJSON string `json:"arguments_json"`
	Text          string `json:"text"`
}

// Parse reads a model's reply into one action and validates it against the offered tools. The
// call's id is made here, never taken from the model.
func Parse(raw []byte, tools []*agentv1.Tool) (*agentv1.StepResponse, error) {
	var r reply
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("not the action's JSON: %w", err)
	}
	switch r.Action {
	case "say", "end":
		if r.Text == "" {
			return nil, fmt.Errorf("%s needs text", r.Action)
		}
		if r.Action == "say" {
			return &agentv1.StepResponse{Action: &agentv1.StepResponse_Say{Say: r.Text}}, nil
		}
		return &agentv1.StepResponse{Action: &agentv1.StepResponse_End{End: r.Text}}, nil
	case "tool_call":
		offered := false
		for _, t := range tools {
			if r.Tool != "" && t.Name == r.Tool {
				offered = true
			}
		}
		if !offered {
			return nil, fmt.Errorf("tool %q is not offered", r.Tool)
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(r.ArgumentsJSON), &args); err != nil || args == nil {
			return nil, errors.New("arguments_json is not a JSON object")
		}
		id, err := callID()
		if err != nil {
			return nil, err
		}
		return &agentv1.StepResponse{Action: &agentv1.StepResponse_ToolCall{ToolCall: &agentv1.ToolCall{
			CallId: id, Name: r.Tool, ArgumentsJson: r.ArgumentsJSON,
		}}}, nil
	}
	return nil, fmt.Errorf("unknown action %q", r.Action)
}

func callID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "c" + hex.EncodeToString(b), nil
}
