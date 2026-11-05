package calls

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"concierge/engine/internal/secrets"
)

// OpenAI is the owner's OpenAI key, set in Voice's setup, and what calld does with it: Keys keeps it
// (secretd, as `openai` in calld's namespace), Checker asks OpenAI whether a key works before it is kept, and
// Open opens a voice session with the key stored as the call starts, so a replaced key is the next
// call's with no restart. The key never reaches a log, an error or an answer but masked.
type OpenAI struct {
	Keys    *secrets.Client
	Checker *KeyChecker
	Open    func(ctx context.Context, key string, s SessionSpec) (Talker, error)
}

var (
	// ErrKeyRefused is OpenAI answering 401 or 403: the key is not one it accepts.
	ErrKeyRefused = errors.New("OpenAI refused this key")
	// ErrOpenAIUnreachable is anything else that stops a 200: the network, a timeout, another status.
	ErrOpenAIUnreachable = errors.New("couldn't reach OpenAI")
)

// KeyChecker asks OpenAI whether a key works: GET {BaseURL}/v1/models with the key as Bearer.
type KeyChecker struct {
	BaseURL string // https://api.openai.com; a loopback fake in tests
	Client  *http.Client
}

func NewKeyChecker() *KeyChecker {
	return &KeyChecker{BaseURL: "https://api.openai.com", Client: &http.Client{Timeout: 10 * time.Second}}
}

// Check is nil when OpenAI accepts key, ErrKeyRefused or ErrOpenAIUnreachable otherwise. What it
// logs is the transport's error, which names the URL, never the key in the header.
func (c *KeyChecker) Check(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/models", nil)
	if err != nil {
		return ErrOpenAIUnreachable
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := c.Client.Do(req)
	if err != nil {
		slog.Warn("calls: OpenAI didn't answer the key check", "err", err)
		return ErrOpenAIUnreachable
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrKeyRefused
	default:
		slog.Warn("calls: OpenAI's key check answered", "status", resp.StatusCode)
		return ErrOpenAIUnreachable
	}
}
