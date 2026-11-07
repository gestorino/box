package agent

import (
	"fmt"
	"strings"
	"time"

	agentv1 "concierge/engine/internal/gen/agent/v1"
)

// Render writes the conversation as the plain text a CLI reads: the time and channel, each event
// as one tagged line, then the tools with their schemas.
func Render(req *agentv1.StepRequest) string {
	var b strings.Builder
	now := req.GetNow().AsTime()
	zone := req.GetTimeZone()
	if loc, err := time.LoadLocation(zone); err == nil {
		now = now.In(loc)
	} else {
		now = now.UTC()
	}
	fmt.Fprintf(&b, "Now: %s (%s)\nChannel: %s\n\nConversation:\n",
		now.Format("2006-01-02 15:04 MST"), line(zone), line(req.GetChannel()))
	for _, e := range req.GetEvents() {
		switch k := e.GetKind().(type) {
		case *agentv1.Event_Owner:
			fmt.Fprintf(&b, "[owner #%d] %s\n", e.GetId(), line(k.Owner))
		case *agentv1.Event_Agent:
			fmt.Fprintf(&b, "[you] %s\n", line(k.Agent))
		case *agentv1.Event_ToolCall:
			fmt.Fprintf(&b, "[tool %s #%s] %s\n", line(k.ToolCall.GetName()), line(k.ToolCall.GetCallId()), line(k.ToolCall.GetArgumentsJson()))
		case *agentv1.Event_ToolResult:
			fmt.Fprintf(&b, "[result #%s] %s\n", line(k.ToolResult.GetCallId()), line(k.ToolResult.GetResultJson()))
		case *agentv1.Event_Errand:
			x := k.Errand
			fmt.Fprintf(&b, "[errand %d %s] %s", x.GetRun(), line(x.GetKind()), line(x.GetText()))
			if len(x.GetOptions()) > 0 {
				fmt.Fprintf(&b, " options: %s", line(strings.Join(x.GetOptions(), " | ")))
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

// lineBreaks are every line break a reader may honour: ASCII's and Unicode's (NEL, LS, PS).
var lineBreaks = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\u0085", " ", " ", " ", " ", " ")

// line keeps a text on one line, so no event's words can pose as another event's tag.
func line(s string) string {
	return lineBreaks.Replace(s)
}
