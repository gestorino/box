package calls

import "concierge/engine/internal/realtime"

// Price is a model's list price per kind of token, in USD per million.
type Price struct {
	TextIn, CachedTextIn, TextOut, AudioIn, CachedAudioIn, AudioOut float64
}

// prices are OpenAI's list prices (https://developers.openai.com/api/docs/pricing), as checked on
// pricesChecked: the API reports tokens but never a price. A model not here is shown unpriced.
var prices = map[string]Price{
	"gpt-realtime-2.1":      {TextIn: 4.00, CachedTextIn: 0.40, TextOut: 24.00, AudioIn: 32.00, CachedAudioIn: 0.40, AudioOut: 64.00},
	"gpt-realtime-2.1-mini": {TextIn: 0.60, CachedTextIn: 0.06, TextOut: 2.40, AudioIn: 10.00, CachedAudioIn: 0.30, AudioOut: 20.00},
	"gpt-realtime-2":        {TextIn: 4.00, CachedTextIn: 0.40, TextOut: 24.00, AudioIn: 32.00, CachedAudioIn: 0.40, AudioOut: 64.00},
	// One input price for all of its input tokens, audio and text alike.
	"gpt-4o-transcribe": {TextIn: 2.50, TextOut: 10.00, AudioIn: 2.50},
}

const pricesChecked = "2026-10-29"

// ModelUsage is one model's tokens on a call and what they cost; CostUSD is nil when the model has
// no price in the table.
type ModelUsage struct {
	realtime.Tokens
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

// CallUsage is what a held call's session used: the voice model and the transcriber, each priced.
type CallUsage struct {
	Call          ModelUsage `json:"call"`
	Transcription ModelUsage `json:"transcription"`
	PricesChecked string     `json:"prices_checked"`
}

// costed prices u, or is nil when the session used no tokens at all.
func costed(u realtime.Usage) *CallUsage {
	if used(u.Call) == 0 && used(u.Transcription) == 0 {
		return nil
	}
	return &CallUsage{Call: priced(u.Call), Transcription: priced(u.Transcription), PricesChecked: pricesChecked}
}

func used(t realtime.Tokens) int {
	return t.TextIn + t.CachedTextIn + t.AudioIn + t.CachedAudioIn + t.TextOut + t.AudioOut
}

func priced(t realtime.Tokens) ModelUsage {
	p, ok := prices[t.Model]
	if !ok {
		return ModelUsage{Tokens: t}
	}
	cost := (float64(t.TextIn)*p.TextIn + float64(t.CachedTextIn)*p.CachedTextIn + float64(t.TextOut)*p.TextOut +
		float64(t.AudioIn)*p.AudioIn + float64(t.CachedAudioIn)*p.CachedAudioIn + float64(t.AudioOut)*p.AudioOut) / 1e6
	return ModelUsage{Tokens: t, CostUSD: &cost}
}
