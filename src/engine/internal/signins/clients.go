package signins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"concierge/engine/internal/h2c"
)

// Mailbox is emaild. Address is its `GET /email/status`: the archived mailbox's address, "" when the Email
// connector is not connected (the box then has no plus-address to register with).
type Mailbox interface {
	Address(ctx context.Context) (string, error)
}

// UplinkReader is hostd's `GET /uplink`: wifi, mobile, wired or none.
type UplinkReader interface {
	Uplink(ctx context.Context) (string, error)
}

// Owner is the Account's names and phone, the only fields of it read here.
// ContactPhone is the owner's own phone (Contacts), where the box reaches them, not the box's SIM.
type Owner struct{ FirstName, LastName, Phone, ContactPhone string }

// OwnerReader is hostd's Account: the owner's names for a form, and the phone, the box's SIM, the number sign-ups give.
type OwnerReader interface {
	Owner(ctx context.Context) (Owner, error)
}

// socketClient is unencrypted HTTP/2 to one daemon's Unix socket; the host in the URL is only a label.
type socketClient struct {
	host string
	http *http.Client
}

func newSocketClient(socket, host string) socketClient {
	return socketClient{host: host, http: &http.Client{Transport: h2c.Transport(socket), Timeout: 5 * time.Second}}
}

// get decodes a 200's JSON body into out. A daemon's error body is not echoed: it is the daemon's
// to log, and an email daemon's could name the mailbox.
func (c socketClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.host+path, nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s: %s", c.host, req.URL.Path, res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

type MailClient struct{ c socketClient }

func NewMailClient(socket string) *MailClient {
	return &MailClient{c: newSocketClient(socket, "emaild")}
}

// Address reads emaild's status object (internal/email/service.go Status). A mailbox that is
// disconnected or refused its password keeps its address in the status, but nothing arrives at a
// plus-address of it, so it counts as no mailbox.
func (m *MailClient) Address(ctx context.Context) (string, error) {
	var st struct {
		State   string `json:"state"`
		Address string `json:"address"`
	}
	if err := m.c.get(ctx, "/email/status", &st); err != nil {
		return "", err
	}
	if st.State == "disconnected" || st.State == "auth_failed" {
		return "", nil
	}
	return st.Address, nil
}

// HostdClient reads hostd's console socket: the uplink, and the Account's owner.
type HostdClient struct{ c socketClient }

func NewHostdClient(socket string) *HostdClient {
	return &HostdClient{c: newSocketClient(socket, "hostd")}
}

func (h *HostdClient) Uplink(ctx context.Context) (string, error) {
	var out struct {
		Uplink string `json:"uplink"`
	}
	if err := h.c.get(ctx, "/uplink", &out); err != nil {
		return "", err
	}
	return out.Uplink, nil
}

// Owner decodes only the names and the phone; the rest of the Account (addresses, card) is never kept here.
func (h *HostdClient) Owner(ctx context.Context) (Owner, error) {
	var out struct {
		FirstName string `json:"owner_first_name"`
		LastName  string `json:"owner_last_name"`
		Phone     string `json:"phone"`
		Contacts  struct {
			Phone string `json:"phone"`
		} `json:"contacts"`
	}
	if err := h.c.get(ctx, "/settings/account", &out); err != nil {
		return Owner{}, err
	}
	return Owner{FirstName: out.FirstName, LastName: out.LastName, Phone: out.Phone, ContactPhone: out.Contacts.Phone}, nil
}

// AgentStates is each agent's connector state in hostd's `/connectors` (not_added, setting_up or
// added), by name.
func (h *HostdClient) AgentStates(ctx context.Context) (map[string]string, error) {
	var out struct {
		Connectors []struct {
			Name  string `json:"name"`
			State string `json:"state"`
			Kind  string `json:"kind"`
		} `json:"connectors"`
	}
	if err := h.c.get(ctx, "/connectors", &out); err != nil {
		return nil, err
	}
	states := map[string]string{}
	for _, c := range out.Connectors {
		if c.Kind == "agent" {
			states[c.Name] = c.State
		}
	}
	return states, nil
}

// AgentSettings is hostd's `/settings/agent`: the primary agent and the secondary ("" for none),
// and whether the owner answers on WhatsApp.
func (h *HostdClient) AgentSettings(ctx context.Context) (primary, secondary string, whatsapp bool, err error) {
	var out struct {
		Primary   string `json:"primary"`
		Secondary string `json:"secondary"`
		Answer    struct {
			WhatsApp bool `json:"whatsapp"`
		} `json:"answer"`
	}
	if err := h.c.get(ctx, "/settings/agent", &out); err != nil {
		return "", "", false, err
	}
	return out.Primary, out.Secondary, out.Answer.WhatsApp, nil
}
