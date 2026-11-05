// Package aikey keeps the owner's OpenRouter key, the one key every sign-in run and errand uses:
// in secretd (the engine's `openrouter`), checked with OpenRouter before it is kept. The key never
// reaches a log, an error or an API answer; the engine hands it to the worker in each request that
// runs a model.
package aikey

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"
)

var (
	// ErrRefused is OpenRouter answering 401 or 403: the key is not one it accepts.
	ErrRefused = errors.New("OpenRouter refused this key")
	// ErrUnreachable is anything else that stops an answer: the network, a timeout, a 5xx.
	ErrUnreachable = errors.New("Couldn't reach OpenRouter")
)

// Checker asks OpenRouter whether a key works and what credit it has left.
type Checker struct {
	BaseURL string // https://openrouter.ai/api/v1; a loopback fake in tests
	Client  *http.Client
}

func NewChecker() *Checker {
	return &Checker{BaseURL: "https://openrouter.ai/api/v1", Client: &http.Client{Timeout: 10 * time.Second}}
}

// Check asks OpenRouter's /key whether key works: nil on 200, ErrRefused or ErrUnreachable
// otherwise. credit is the USD left: the key's own limit left when it has one, else the account's
// credits less its usage (/credits); nil when neither can be read, which doesn't fail the check.
func (c *Checker) Check(ctx context.Context, key string) (credit *float64, err error) {
	var k struct {
		Data struct {
			LimitRemaining *float64 `json:"limit_remaining"`
		} `json:"data"`
	}
	status, err := c.get(ctx, "/key", key, &k)
	switch {
	case err != nil:
		return nil, ErrUnreachable
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return nil, ErrRefused
	case status != http.StatusOK:
		slog.Warn("aikey: OpenRouter's key check answered", "status", status)
		return nil, ErrUnreachable
	}
	if left := k.Data.LimitRemaining; left != nil {
		return cents(*left), nil
	}
	var cr struct {
		Data struct {
			TotalCredits *float64 `json:"total_credits"`
			TotalUsage   *float64 `json:"total_usage"`
		} `json:"data"`
	}
	if status, err := c.get(ctx, "/credits", key, &cr); err != nil || status != http.StatusOK ||
		cr.Data.TotalCredits == nil || cr.Data.TotalUsage == nil {
		return nil, nil
	}
	return cents(*cr.Data.TotalCredits - *cr.Data.TotalUsage), nil
}

func cents(usd float64) *float64 {
	v := math.Round(usd*100) / 100
	return &v
}

// get calls OpenRouter with key and decodes a 200's body into out (a body it can't read leaves out
// as it was). err is only a failure to get an answer: the transport's, which names the URL, never
// the key in the header.
func (c *Checker) get(ctx context.Context, path, key string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := c.Client.Do(req)
	if err != nil {
		slog.Warn("aikey: OpenRouter didn't answer", "path", path, "err", err)
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			slog.Warn("aikey: OpenRouter's answer unreadable", "path", path, "err", err)
		}
	}
	return resp.StatusCode, nil
}
