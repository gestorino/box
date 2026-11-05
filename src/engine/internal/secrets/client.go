package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"concierge/engine/internal/h2c"
)

// ErrUnavailable is secretd not answering: no socket, no reply in time, a failure on its side, or a
// refusal (not a caller).
// Callers refuse the work that needs a secret; nothing falls back to another store.
var ErrUnavailable = errors.New("secrets unavailable")

// Client is a service's way to its own namespace in secretd (which namespace is the kernel's call,
// by the socket's peer). It never logs a value and never puts one in an error.
type Client struct {
	http *http.Client
}

func NewClient(socket string) *Client {
	return &Client{http: &http.Client{Transport: h2c.Transport(socket), Timeout: 5 * time.Second}}
}

// Get returns the secret's value; an absent secret is ("", false, nil).
func (c *Client) Get(ctx context.Context, name string) (string, bool, error) {
	status, body, err := c.do(ctx, http.MethodGet, "/secrets/"+name, nil)
	if err != nil {
		return "", false, err
	}
	switch status {
	case http.StatusOK:
		var out struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return "", false, fmt.Errorf("%w: unreadable answer", ErrUnavailable)
		}
		return out.Value, true, nil
	case http.StatusNotFound:
		return "", false, nil
	}
	return "", false, statusError(status, name)
}

func (c *Client) Put(ctx context.Context, name, value string) error {
	body, err := json.Marshal(struct {
		Value string `json:"value"`
	}{value})
	if err != nil {
		return err
	}
	return c.expect(ctx, http.MethodPut, "/secrets/"+name, body, name)
}

func (c *Client) Delete(ctx context.Context, name string) error {
	return c.expect(ctx, http.MethodDelete, "/secrets/"+name, nil, name)
}

func (c *Client) expect(ctx context.Context, method, path string, body []byte, name string) error {
	status, _, err := c.do(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent {
		return statusError(status, name)
	}
	return nil
}

// do sends one request; a dial failure, a timeout or a 5xx is ErrUnavailable.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	u := url.URL{Scheme: "http", Host: "secretd", Path: path}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, ErrUnavailable // the cause can't hold a value, but the caller needs only this
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil || resp.StatusCode >= 500 {
		return 0, nil, ErrUnavailable
	}
	return resp.StatusCode, answer, nil
}

// statusError: a bad name (400) is the caller's own mistake, a plain error; any other answer secretd
// isn't meant to give (403 not a caller included) leaves the secret out of reach: ErrUnavailable.
// Neither names a value.
func statusError(status int, name string) error {
	if status == http.StatusBadRequest {
		return fmt.Errorf("secretd refused %q: status %d", name, status)
	}
	return fmt.Errorf("%w: secretd refused %q: status %d", ErrUnavailable, name, status)
}
