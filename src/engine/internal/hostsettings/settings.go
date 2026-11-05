// Package hostsettings is how the daemons read the settings hostd owns (internal/hostd/settings.go),
// over its read-only settings socket.
package hostsettings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"concierge/engine/internal/h2c"
)

// Retention is how long SMS, calls and email are kept, in days; 0 keeps them forever. hostd owns it
// (internal/hostd/settings.go); this is its JSON contract.
type Retention struct {
	SMSDays   int `json:"sms_retention_days"`
	CallsDays int `json:"calls_retention_days"`
	EmailDays int `json:"email_retention_days"`
}

// CallsKeep is what the owner lets a call leave behind for debugging: its transcript, its audio, and
// how long either stays. Both switches are off by default; hostd owns them.
type CallsKeep struct {
	Transcript bool `json:"calls_keep_transcript"`
	Audio      bool `json:"calls_keep_audio"`
	DebugDays  int  `json:"calls_debug_days"`
}

// Voice is what the owner sets for the calls the box makes: the realtime model that speaks. hostd
// owns it.
type Voice struct {
	Model string `json:"voice_model"`
}

// Owner is the owner's name, from Account: the names a call says, in English and in Russian (which
// declines it). Names may be empty. hostd owns them; no address or card ever reaches a daemon.
type Owner struct {
	FirstName string `json:"owner_first_name"`
	LastName  string `json:"owner_last_name"`
	NameRu    string `json:"owner_name_ru"`
}

// VoiceModels are the realtime models a call may use; the first is the default.
var VoiceModels = []string{"gpt-realtime-2.1", "gpt-realtime-2.1-mini", "gpt-realtime-2"}

// Settings is what hostd's read-only GET /settings answers: one flat object, the groups side by side.
type Settings struct {
	Retention
	CallsKeep
	Voice
	Owner
}

// Client reads the settings from hostd's read-only settings socket and keeps the last value
// it saw, so hostd being away never changes what is purged. Until hostd first answers the retention
// is unknown, and nothing is purged on a guess.
type Client struct {
	every  time.Duration
	client *http.Client

	mu    sync.Mutex
	last  Settings
	known bool
}

func NewClient(socket string, every time.Duration) *Client {
	return &Client{
		every:  every,
		client: &http.Client{Transport: h2c.Transport(socket), Timeout: 5 * time.Second},
	}
}

func (c *Client) Retention() (Retention, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last.Retention, c.known
}

// CallsKeep is the last keep switches hostd gave; unknown until it first answers, and a call started
// before then keeps nothing.
func (c *Client) CallsKeep() (CallsKeep, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last.CallsKeep, c.known
}

// Voice is the last voice settings hostd gave; unknown until it first answers.
func (c *Client) Voice() (Voice, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last.Voice, c.known
}

// Owner is the last owner names hostd gave; unknown until it first answers.
func (c *Client) Owner() (Owner, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last.Owner, c.known
}

// Uplink asks hostd which link the box's traffic leaves by now: wifi, mobile, wired or none.
func (c *Client) Uplink(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://hostd/uplink", nil)
	res, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		Uplink string `json:"uplink"`
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("hostd uplink: %s", res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Uplink, nil
}

// Run reads now and every `every` until ctx ends.
func (c *Client) Run(ctx context.Context) {
	t := time.NewTicker(c.every)
	defer t.Stop()
	for {
		c.Refresh(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Refresh reads the settings now, for a caller that must act on the current value rather than one up
// to a tick old (a call deciding what it keeps). If hostd doesn't answer, the last value seen stands.
func (c *Client) Refresh(ctx context.Context) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://hostd/settings", nil)
	res, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			slog.Debug("hostsettings: settings unreachable, keeping the last known", "err", err)
		}
		return
	}
	defer res.Body.Close()
	var r Settings
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&r) != nil {
		slog.Warn("hostsettings: settings unreadable, keeping the last known", "status", res.StatusCode)
		return
	}
	c.mu.Lock()
	retentionChanged, keepChanged := !c.known || c.last.Retention != r.Retention, !c.known || c.last.CallsKeep != r.CallsKeep
	c.last, c.known = r, true
	c.mu.Unlock()
	if retentionChanged {
		slog.Info("hostsettings: retention", "sms_days", r.SMSDays, "calls_days", r.CallsDays)
	}
	if keepChanged {
		slog.Info("hostsettings: calls keep", "transcript", r.Transcript, "audio", r.Audio, "debug_days", r.DebugDays)
	}
}
