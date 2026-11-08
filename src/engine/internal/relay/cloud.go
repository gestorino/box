package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The control plane's refusals of a claim, told apart.
var (
	ErrWrongCode      = errors.New("wrong code")
	ErrExpiredCode    = errors.New("expired code")
	ErrNoCode         = errors.New("no code")
	ErrAlreadyClaimed = errors.New("box already claimed")

	// ErrUnreachable is no answer from the control plane at all: no network, no route, no reply.
	ErrUnreachable = errors.New("control plane unreachable")
)

// ClaimResult is what the control plane answers a finished claim with.
type ClaimResult struct {
	Name, BoxDomain, Relay string
}

// Cloud is the client of the control plane's /box/v1 contract. Its errors never carry the email, the
// code or the box secret.
type Cloud struct {
	base      string
	version   string
	boxDomain string
	hc        *http.Client
}

// NewCloud makes a client of the control plane at base (https://<site-domain>); version is the
// box's, sent with every status call; boxDomain is the only domain a claim may put the box under
// (the box's secret goes to relay.<box-domain>, so the control plane can't send it elsewhere).
func NewCloud(base, version, boxDomain string) *Cloud {
	return &Cloud{base: strings.TrimRight(base, "/"), version: version, boxDomain: boxDomain,
		hc: &http.Client{Timeout: 15 * time.Second,
			// A redirect is the answer, never followed: following one would hand the box's headers
			// (or a re-POSTed email, code and secret) to its target. Every 3xx is then an error.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// do sends one request and returns the status and the body's error string (if any). A transport
// failure is reported without the URL's or the body's content.
func (c *Cloud) do(ctx context.Context, method, path string, body any, hdr map[string]string, out any) (int, string, error) {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, "", errors.New("cloud: encode request")
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, "", errors.New("cloud: build request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		return 0, "", ErrUnreachable
	}
	defer resp.Body.Close()
	var reply struct {
		Error string `json:"error"`
	}
	dec := json.NewDecoder(resp.Body)
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := dec.Decode(out); err != nil {
			return resp.StatusCode, "", errors.New("cloud: unreadable answer")
		}
	} else {
		_ = dec.Decode(&reply)
	}
	return resp.StatusCode, reply.Error, nil
}

// ClaimStart asks the control plane to mail the email's account a code.
func (c *Cloud) ClaimStart(ctx context.Context, email string) error {
	st, _, err := c.do(ctx, "POST", "/box/v1/claim/start", map[string]string{"email": email}, nil, nil)
	if err != nil {
		return err
	}
	if st != http.StatusOK {
		return fmt.Errorf("cloud: claim start answered %d", st)
	}
	return nil
}

// ClaimFinish claims the box with the mailed code.
func (c *Cloud) ClaimFinish(ctx context.Context, email, code, boxID, secret string) (ClaimResult, error) {
	var out struct {
		Name      string `json:"name"`
		BoxDomain string `json:"box_domain"`
		Relay     string `json:"relay"`
	}
	st, msg, err := c.do(ctx, "POST", "/box/v1/claim/finish",
		map[string]string{"email": email, "code": code, "box_id": boxID, "secret": secret}, nil, &out)
	if err != nil {
		return ClaimResult{}, err
	}
	switch {
	case st == http.StatusOK:
		if !isLabel(out.Name) || out.BoxDomain != c.boxDomain || out.Relay != "relay."+c.boxDomain+":7000" {
			return ClaimResult{}, errors.New("cloud: claim finish answered a name or relay outside the box domain")
		}
		return ClaimResult{Name: out.Name, BoxDomain: out.BoxDomain, Relay: out.Relay}, nil
	case st == http.StatusConflict:
		return ClaimResult{}, ErrAlreadyClaimed
	case st == http.StatusBadRequest:
		switch msg {
		case "wrong code":
			return ClaimResult{}, ErrWrongCode
		case "expired code":
			return ClaimResult{}, ErrExpiredCode
		case "no code":
			return ClaimResult{}, ErrNoCode
		}
	}
	return ClaimResult{}, fmt.Errorf("cloud: claim finish answered %d", st)
}

// Status asks whether the control plane still holds the box as claimed. A 401 means it was removed
// (false, nil); any other answer, a 200 that does not say claimed included, is an error.
func (c *Cloud) Status(ctx context.Context, boxID, secret string) (bool, error) {
	var out struct {
		Claimed bool `json:"claimed"`
	}
	st, _, err := c.do(ctx, "GET", "/box/v1/status", nil, map[string]string{
		"X-Box-Id": boxID, "X-Box-Secret": secret, "X-Box-Version": c.version,
	}, &out)
	if err != nil {
		return false, err
	}
	switch st {
	case http.StatusOK:
		if !out.Claimed {
			return false, errors.New("cloud: status answered 200 but not claimed")
		}
		return true, nil
	case http.StatusUnauthorized:
		return false, nil
	}
	return false, fmt.Errorf("cloud: status answered %d", st)
}

// isLabel says name is one DNS label as the control plane makes them: lowercase letters, digits and
// inner hyphens, at most 63.
func isLabel(name string) bool {
	if name == "" || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
