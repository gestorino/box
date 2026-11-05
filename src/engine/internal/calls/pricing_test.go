package calls

import (
	"math"
	"testing"

	"concierge/engine/internal/realtime"
)

func near(got *float64, want float64) bool { return got != nil && math.Abs(*got-want) < 1e-9 }

// Each kind of token at its own price (OpenAI pricing, checked 2026-10-29): a million of each.
func TestTheCallAndTheTranscriptionArePricedPerTokenKind(t *testing.T) {
	m := 1_000_000
	u := costed(realtime.Usage{
		Call: realtime.Tokens{Model: "gpt-realtime-2.1", TextIn: m, CachedTextIn: m, AudioIn: m, CachedAudioIn: m, TextOut: m, AudioOut: m},
		// The page gives gpt-4o-transcribe one input price, for audio and text alike.
		Transcription: realtime.Tokens{Model: "gpt-4o-transcribe", TextIn: m / 2, AudioIn: m / 2, TextOut: m},
	})
	if !near(u.Call.CostUSD, 4+0.40+32+0.40+24+64) {
		t.Fatalf("call cost %v", u.Call.CostUSD)
	}
	if !near(u.Transcription.CostUSD, 2.50+10) {
		t.Fatalf("transcription cost %v", u.Transcription.CostUSD)
	}
	if u.PricesChecked != "2026-10-29" || u.Call.Model != "gpt-realtime-2.1" || u.Call.AudioOut != m {
		t.Fatalf("%+v", u)
	}
}

// A model the price list doesn't know keeps its tokens and shows no cost: never a guessed figure.
func TestAModelWithoutAPriceHasNoCost(t *testing.T) {
	u := costed(realtime.Usage{Call: realtime.Tokens{Model: "gpt-realtime-9", AudioIn: 10}})
	if u.Call.CostUSD != nil || u.Call.AudioIn != 10 {
		t.Fatalf("%+v", u.Call)
	}
}

// A session that used nothing has nothing to keep.
func TestNoTokensNoUsage(t *testing.T) {
	if costed(realtime.Usage{Call: realtime.Tokens{Model: "gpt-realtime-2.1"}}) != nil {
		t.Fatal("usage for a session that used nothing")
	}
}
