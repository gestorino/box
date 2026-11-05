package signins

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"concierge/engine/internal/secrets"
)

// Every run needs the OpenRouter key: without it none starts, rather than one failing halfway.
func TestNoRunStartsWithoutTheKey(t *testing.T) {
	f := newFixture(t)
	f.setKey(t, false)
	ctx := context.Background()
	if _, err := f.svc.StartErrand(ctx, Errand{URL: "https://shop.test/x", Action: "do it"}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("errand: %v", err)
	}
	f.svc.runs.Wait()
	if runs, _ := f.store.Runs(ctx, shopKey); len(runs) != 0 || len(f.worker.sent()) != 0 {
		t.Fatalf("runs %v, errands %d", runs, len(f.worker.sent()))
	}
	f.setKey(t, true)
	if _, err := f.svc.StartErrand(ctx, Errand{URL: "https://shop.test/x", Action: "do it"}); err != nil {
		t.Fatalf("errand with the key: %v", err)
	}
	f.svc.runs.Wait()
}

// A service nobody told about the key has none: the gate is shut by default.
func TestAServiceWithoutAKeyReaderHasNoKey(t *testing.T) {
	f := newFixture(t)
	f.svc.cfg.HasKey = nil
	if _, err := f.svc.StartErrand(context.Background(), Errand{URL: "https://shop.test/x", Action: "do it"}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("errand: %v", err)
	}
}

func TestTheRoutesThatStartARunAnswer409WithoutTheKey(t *testing.T) {
	f := newFixture(t)
	f.setKey(t, false)
	code, out := runsCall(t, NewRunsHandler(f.svc), "POST", "/api/v1/errands", `{"url":"https://shop.test/x","action":"do it"}`)
	if code != http.StatusConflict || out["error"] != "no OpenRouter key" {
		t.Fatalf("errand: %d %v", code, out)
	}
}

// secretd down: a run doesn't start, and says why — never that the key is missing.
func TestRunRefusedWhenSecretsUnavailable(t *testing.T) {
	f := newFixture(t)
	f.secretdDown(t)
	ctx := context.Background()
	if _, err := f.svc.StartErrand(ctx, Errand{URL: "https://shop.test/x", Action: "do it"}); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("errand: %v", err)
	}
	if _, err := f.svc.Register(ctx, shopKey); !errors.Is(err, secrets.ErrUnavailable) {
		t.Fatalf("register: %v", err)
	}
	f.svc.runs.Wait()
	if runs, _ := f.store.Runs(ctx, shopKey); len(runs) != 0 || len(f.worker.sent()) != 0 {
		t.Fatalf("runs %v, errands %d", runs, len(f.worker.sent()))
	}
	code, out := runsCall(t, NewRunsHandler(f.svc), "POST", "/api/v1/errands", `{"url":"https://shop.test/x","action":"do it"}`)
	if code != http.StatusBadGateway || out["error"] != "secrets unavailable" {
		t.Fatalf("route: %d %v", code, out)
	}
}
