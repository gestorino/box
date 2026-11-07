package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"concierge/engine/internal/agent"
	agentv1 "concierge/engine/internal/gen/agent/v1"
)

var tools = []*agentv1.Tool{{Name: "list_accounts", Description: "the sites", ParametersJson: `{"type":"object"}`}}

func TestParseATooledAction(t *testing.T) {
	resp, err := agent.Parse([]byte(`{"action":"tool_call","tool":"list_accounts","arguments_json":"{}","text":""}`), tools)
	if err != nil {
		t.Fatal(err)
	}
	c := resp.GetToolCall()
	if c == nil || c.Name != "list_accounts" || c.ArgumentsJson != "{}" || c.CallId == "" {
		t.Fatalf("got %v", resp)
	}
}

func TestParseRefusesWhatIsNotAnAction(t *testing.T) {
	for _, raw := range []string{
		`{"action":"tool_call","tool":"rm","arguments_json":"{}","text":""}`,
		`{"action":"tool_call","tool":"","arguments_json":"{}","text":""}`,
		`{"action":"tool_call","tool":"list_accounts","arguments_json":"[1]","text":""}`,
		`{"action":"tool_call","tool":"list_accounts","arguments_json":"nope","text":""}`,
		`{"action":"dance","tool":"","arguments_json":"{}","text":"x"}`,
		`sure!`,
	} {
		if _, err := agent.Parse([]byte(raw), tools); err == nil {
			t.Errorf("%s: want an error", raw)
		}
	}
}

func TestParseSayAndEnd(t *testing.T) {
	r, err := agent.Parse([]byte(`{"action":"say","tool":"","arguments_json":"{}","text":"hi"}`), nil)
	if err != nil || r.GetSay() != "hi" {
		t.Fatalf("say: %v %v", r, err)
	}
	r, err = agent.Parse([]byte(`{"action":"end","tool":"","arguments_json":"{}","text":"bye"}`), nil)
	if err != nil || r.GetEnd() != "bye" {
		t.Fatalf("end: %v %v", r, err)
	}
	if _, err = agent.Parse([]byte(`{"action":"say","tool":"","arguments_json":"{}","text":""}`), nil); err == nil {
		t.Fatal("empty text must fail")
	}
}

func TestCallIDsAreUnique(t *testing.T) {
	raw := []byte(`{"action":"tool_call","tool":"list_accounts","arguments_json":"{}","text":""}`)
	a, _ := agent.Parse(raw, tools)
	b, _ := agent.Parse(raw, tools)
	if a.GetToolCall().CallId == b.GetToolCall().CallId {
		t.Fatal("call ids repeat")
	}
}

func TestActionSchemaIsStrict(t *testing.T) {
	var s struct {
		Type                 string                    `json:"type"`
		Required             []string                  `json:"required"`
		AdditionalProperties bool                      `json:"additionalProperties"`
		Properties           map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(agent.ActionSchema(tools), &s); err != nil {
		t.Fatal(err)
	}
	if s.Type != "object" || s.AdditionalProperties || len(s.Required) != 4 || len(s.Properties) != 4 {
		t.Fatalf("schema: %+v", s)
	}
	if got, _ := json.Marshal(s.Properties["tool"]["enum"]); string(got) != `["list_accounts",""]` {
		t.Fatalf("tool enum %s", got)
	}
	if got, _ := json.Marshal(s.Properties["action"]["enum"]); string(got) != `["tool_call","say","end"]` {
		t.Fatalf("action enum %s", got)
	}
	var none struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(agent.ActionSchema(nil), &none); err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(none.Properties["tool"]["enum"]); string(got) != `[""]` {
		t.Fatalf("empty enum %s", got)
	}
}

type fakeRunner struct {
	replies []string
	usage   *agentv1.Usage
	inputs  []string
}

func (f *fakeRunner) Run(_ context.Context, _, input string, _ []byte) ([]byte, *agentv1.Usage, error) {
	f.inputs = append(f.inputs, input)
	if len(f.inputs) > len(f.replies) {
		return nil, nil, errors.New("too many runs")
	}
	return []byte(f.replies[len(f.inputs)-1]), f.usage, nil
}

func TestABadReplyIsRetriedOnceThenAnError(t *testing.T) {
	r := &fakeRunner{replies: []string{"sure!", "{bad"}}
	_, err := agent.NewServer(r, nil).Step(context.Background(), connect.NewRequest(&agentv1.StepRequest{}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("code %v (%v)", connect.CodeOf(err), err)
	}
	if len(r.inputs) != 2 {
		t.Fatalf("runs: %d", len(r.inputs))
	}
	if !strings.Contains(r.inputs[1], "Your reply was not valid:") || !strings.Contains(r.inputs[1], "Reply with exactly one action.") {
		t.Fatalf("retry input: %q", r.inputs[1])
	}
}

func TestStepCarriesTheRunnersUsage(t *testing.T) {
	u := &agentv1.Usage{Model: "m", InputTokens: 3, OutputTokens: 4, CostUsd: 0.5, CostKnown: true}
	r := &fakeRunner{replies: []string{"nope", `{"action":"say","tool":"","arguments_json":"{}","text":"ok"}`}, usage: u}
	resp, err := agent.NewServer(r, nil).Step(context.Background(), connect.NewRequest(&agentv1.StepRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetSay() != "ok" || !proto.Equal(resp.Msg.Usage, u) {
		t.Fatalf("got %v", resp.Msg)
	}
}

func TestRenderShowsEventIDsAndOptions(t *testing.T) {
	out := agent.Render(&agentv1.StepRequest{
		Channel:  "whatsapp",
		TimeZone: "Europe/Lisbon",
		Now:      timestamppb.New(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)),
		Tools:    tools,
		Events: []*agentv1.Event{
			{Id: 12, Kind: &agentv1.Event_Owner{Owner: "book it"}},
			{Id: 13, Kind: &agentv1.Event_Agent{Agent: "done"}},
			{Id: 14, Kind: &agentv1.Event_ToolCall{ToolCall: &agentv1.ToolCall{CallId: "c1", Name: "get_x", ArgumentsJson: `{}`}}},
			{Id: 15, Kind: &agentv1.Event_ToolResult{ToolResult: &agentv1.ToolResult{CallId: "c1", ResultJson: `{"a":1}`}}},
			{Id: 16, Kind: &agentv1.Event_Errand{Errand: &agentv1.ErrandEvent{Run: 51, Kind: "question", Text: "When?", Options: []string{"19:30", "21:00"}}}},
		},
	})
	for _, want := range []string{"[owner #12] book it", "[you] done", "[tool get_x #c1] {}", "[result #c1] {\"a\":1}",
		"[errand 51 question] When? options: 19:30 | 21:00", "whatsapp", "Europe/Lisbon", "list_accounts", `{"type":"object"}`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
