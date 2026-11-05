package whatsapp

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// meow is the Client over whatsmeow. Thin on purpose: the behaviour is in Service, tested; this is
// verified on the box. After a logout whatsmeow needs a fresh device to pair again, so the client is
// replaced and the event handler re-attached.
type meow struct {
	container *sqlstore.Container
	log       waLog.Logger

	mu      sync.Mutex
	cli     *whatsmeow.Client
	handler func(any)
}

// NewMeow opens whatsmeow's session store at sessionPath (pure-Go SQLite) and its first device.
func NewMeow(ctx context.Context, sessionPath string, log waLog.Logger) (Client, error) {
	identifyDevice()
	container, err := sqlstore.New(ctx, "sqlite",
		"file:"+sessionPath+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", log)
	if err != nil {
		return nil, err
	}
	dev, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, err
	}
	return &meow{container: container, log: log, cli: newClient(dev, log)}, nil
}

// newClient retries a failed first connect in the background too (a box booting before its uplink).
func newClient(dev *store.Device, log waLog.Logger) *whatsmeow.Client {
	c := whatsmeow.NewClient(dev, log)
	c.InitialAutoReconnect = true
	return c
}

// identifyDevice makes the box show as "Concierge" under the phone's Linked devices: the phone names a
// device by its platform type, and only a real one (desktop) shows the OS name — whatsmeow's default,
// UNKNOWN, shows as "Other device". It is sent when pairing, so it applies from the next link.
func identifyDevice() {
	store.SetOSInfo("Concierge", [3]uint32{1, 0, 0})
	store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
}

func (m *meow) client() *whatsmeow.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cli
}

func (m *meow) Connect() error { return m.client().Connect() }
func (m *meow) Disconnect()    { m.client().Disconnect() }

func (m *meow) Logout(ctx context.Context) error {
	if err := m.client().Logout(ctx); err != nil {
		return err
	}
	m.fresh()
	return nil
}

func (m *meow) Forget(ctx context.Context) error {
	c := m.client()
	c.Disconnect()
	if c.Store.ID != nil {
		if err := c.Store.Delete(ctx); err != nil {
			return err
		}
	}
	m.fresh()
	return nil
}

// fresh swaps in a client on a new, unpaired device.
func (m *meow) fresh() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cli = newClient(m.container.NewDevice(), m.log)
	if m.handler != nil {
		m.cli.AddEventHandler(m.handler)
	}
}

func (m *meow) QRChannel(ctx context.Context) (<-chan QRItem, error) {
	c := m.client()
	if c.Store.ID != nil {
		return nil, errors.New("already linked")
	}
	in, err := c.GetQRChannel(ctx)
	if err != nil {
		return nil, err
	}
	out := make(chan QRItem)
	go func() {
		defer close(out)
		for item := range in {
			out <- QRItem{Event: item.Event, Code: item.Code, Timeout: item.Timeout}
		}
	}()
	return out, nil
}

func (m *meow) OnEvent(h func(any)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handler = h
	m.cli.AddEventHandler(h)
}

func (m *meow) Account() string {
	if id := m.client().Store.ID; id != nil {
		return id.ToNonAD().String()
	}
	return ""
}

func (m *meow) PushName() string { return m.client().Store.PushName }

func (m *meow) PNForLID(ctx context.Context, lid types.JID) (types.JID, bool) {
	pn, err := m.client().Store.LIDs.GetPNForLID(ctx, lid)
	return pn, err == nil && !pn.IsEmpty()
}

func (m *meow) GroupName(ctx context.Context, jid string) (string, error) {
	parsed, err := types.ParseJID(jid)
	if err != nil {
		return "", err
	}
	info, err := m.client().GetGroupInfo(ctx, parsed)
	if err != nil {
		return "", err
	}
	return info.Name, nil
}

func (m *meow) PresenceUnavailable(ctx context.Context) error {
	return m.client().SendPresence(ctx, types.PresenceUnavailable)
}

func (m *meow) SendText(ctx context.Context, chat types.JID, text string) (string, time.Time, error) {
	resp, err := m.client().SendMessage(ctx, chat, &waE2E.Message{Conversation: proto.String(text)})
	return resp.ID, resp.Timestamp, err
}

func (m *meow) ParseWebMessage(chat types.JID, msg *waWeb.WebMessageInfo) (*events.Message, error) {
	return m.client().ParseWebMessage(chat, msg)
}

func (m *meow) MarkRead(ctx context.Context, ids []string, at time.Time, chat types.JID) error {
	return m.client().MarkRead(ctx, ids, at, chat, types.EmptyJID)
}
