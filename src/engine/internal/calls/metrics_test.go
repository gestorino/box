package calls

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMetricsWriter(t *testing.T) {
	dir := t.TempDir()
	now := t0
	var live []LiveCall
	queries := 0
	w := &MetricsWriter{
		Dir:  dir,
		Live: func(context.Context) ([]LiveCall, error) { return live, nil },
		QTemp: func(context.Context) (string, error) {
			queries++
			return "+QTEMP: 34,35,33", nil
		},
		Now: func() time.Time { return now },
	}
	ctx := context.Background()
	read := func() string {
		b, err := os.ReadFile(filepath.Join(dir, "concierge-modem.prom"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	has := func(out, line string) {
		t.Helper()
		if !strings.Contains(out, line+"\n") {
			t.Errorf("missing %q in\n%s", line, out)
		}
	}

	w.tick(ctx)
	out := read()
	has(out, `concierge_modem_temp_celsius{sensor="pmic"} 34`)
	has(out, `concierge_modem_temp_celsius{sensor="xo"} 35`)
	has(out, `concierge_modem_temp_celsius{sensor="pa"} 33`)
	if strings.Contains(out, "concierge_call_active{") || queries != 1 {
		t.Fatalf("queries %d\n%s", queries, out)
	}

	now = now.Add(10 * time.Second)
	live = []LiveCall{{5, "outgoing"}}
	w.tick(ctx)
	out = read()
	has(out, `concierge_call_active{call="5",direction="out"} 1`)
	has(out, `concierge_modem_temp_celsius{sensor="pmic"} 34`)

	now = t0.Add(120 * time.Second)
	w.tick(ctx)
	if queries != 1 {
		t.Fatalf("queried during a call: %d", queries)
	}

	live = nil
	now = t0.Add(179 * time.Second) // the call ended, and the last query was long ago
	w.tick(ctx)
	if queries != 2 {
		t.Fatalf("a minute passed and a call ended: %d queries", queries)
	}
	now = now.Add(59 * time.Second)
	w.tick(ctx)
	if queries != 2 {
		t.Fatalf("59 s: %d queries", queries)
	}
	now = now.Add(time.Second)
	w.tick(ctx)
	if queries != 3 {
		t.Fatalf("60 s: %d queries", queries)
	}
	if strings.Contains(read(), "concierge_call_active{") {
		t.Fatal("an ended call is still reported")
	}
}

func TestMetricsWriterKeepsTheLastReadingWhenAQueryFails(t *testing.T) {
	dir := t.TempDir()
	now := t0
	fail := false
	w := &MetricsWriter{
		Dir:  dir,
		Live: func(context.Context) ([]LiveCall, error) { return nil, nil },
		QTemp: func(context.Context) (string, error) {
			if fail {
				return "", errors.New("no port")
			}
			return "+QTEMP: 40,41,42", nil
		},
		Now: func() time.Time { return now },
	}
	w.tick(context.Background())
	fail = true
	now = now.Add(time.Minute)
	w.tick(context.Background())
	b, _ := os.ReadFile(filepath.Join(dir, "concierge-modem.prom"))
	if !strings.Contains(string(b), `concierge_modem_temp_celsius{sensor="pa"} 42`) {
		t.Fatalf("%s", b)
	}
}

func TestMetricsWriterRunStopsOnCancel(t *testing.T) {
	w := &MetricsWriter{Dir: t.TempDir(),
		Live:  func(context.Context) ([]LiveCall, error) { return nil, nil },
		QTemp: func(context.Context) (string, error) { return "+QTEMP: 1,2,3", nil },
		Now:   time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}
