package relay

import (
	"log/slog"
	"testing"
)

// SetFrpLogLevel lets frp's lines from level up reach slog, until the test's cleanup.
func SetFrpLogLevel(t testing.TB, level slog.Level) {
	prev := frpLogLevel.Level()
	frpLogLevel.Set(level)
	t.Cleanup(func() { frpLogLevel.Set(prev) })
}
