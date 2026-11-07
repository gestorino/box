package agent

import (
	"fmt"
	"strings"

	agentv1 "concierge/engine/internal/gen/agent/v1"
)

// Render writes the conversation as the plain text a CLI reads: the time and channel, each event
// as one tagged line, then the tools with their schemas.
func Render(req *agentv1.StepRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Now: %s (%s)\nChannel: %s\n\nConversation:\n",
		req.GetNow().AsTime().Format("2006-01-02 15:04 MST"), req.GetTimeZone(), req.GetChannel())
	for _, e := range req.GetEvents() {
		switch k := e.GetKind().(type) {
		case *agentv1.Event_Owner:
			fmt.Fprintf(&b, "[owner #%d] %s\n", e.GetId(), k.Owner)
		case *agentv1.Event_Agent:
			fmt.Fprintf(&b, "[you] %s\n", k.Agent)
		case *agentv1.Event_ToolCall:
			fmt.Fprintf(&b, "[tool %s #%s] %s\n", k.ToolCall.GetName(), k.ToolCall.GetCallId(), k.ToolCall.GetArgumentsJson())
		case *agentv1.Event_ToolResult:
			fmt.Fprintf(&b, "[result #%s] %s\n", k.ToolResult.GetCallId(), k.ToolResult.GetResultJson())
		case *agentv1.Event_Errand:
			x := k.Errand
			fmt.Fprintf(&b, "[errand %d %s] %s", x.GetRun(), x.GetKind(), x.GetText())
			if len(x.GetOptions()) > 0 {
				fmt.Fprintf(&b, " options: %s", strings.Join(x.GetOptions(), " | "))
			}
			b.WriteByte('\n')
		}
	}
	b.WriteString("\nTools:\n")
	if len(req.GetTools()) == 0 {
		b.WriteString("(none)\n")
	}
	for _, t := range req.GetTools() {
		fmt.Fprintf(&b, "- %s: %s\n  parameters: %s\n", t.GetName(), t.GetDescription(), t.GetParametersJson())
	}
	return b.String()
}
