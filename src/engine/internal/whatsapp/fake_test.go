package whatsapp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// fakeClient stands in for whatsmeow: tests drive its QR channel and emit events.
type fakeClient struct {
	mu        sync.Mutex
	calls     []string
	account   string
	pushName  string
	qr        chan QRItem
	handler   func(any)
	groups    map[string]string
	lids      map[types.JID]types.JID // LID → phone number
	connErrs  []error                 // Connect fails with these first, in order
	logoutErr error                   // Logout fails with this (the box can't reach WhatsApp)
	sendErr   error                   // SendText fails with this
	markErr   error                   // MarkRead fails with this
	markStuck bool                    // MarkRead blocks until its context ends
}

// MarkRead records only the marks that went through.
func (f *fakeClient) MarkRead(ctx context.Context, ids []string, _ time.Time, chat types.JID) error {
	f.mu.Lock()
	err, stuck := f.markErr, f.markStuck
	f.mu.Unlock()
	if stuck {
		<-ctx.Done()
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	f.record("MarkRead " + chat.String() + " " + strings.Join(ids, ","))
	return nil
}

func (f *fakeClient) stickMarks(stuck bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markStuck = stuck
}

func (f *fakeClient) failMarks(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markErr = err
}

func (f *fakeClient) SendText(_ context.Context, chat types.JID, text string) (string, time.Time, error) {
	f.record("SendText " + chat.String() + " " + text)
	if f.sendErr != nil {
		return "", time.Time{}, f.sendErr
	}
	return "S1", t0, nil
}

func newFakeClient() *fakeClient {
	return &fakeClient{groups: map[string]string{}, lids: map[types.JID]types.JID{}}
}

func (f *fakeClient) record(c string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
}

func (f *fakeClient) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeClient) count(c string) int {
	n := 0
	for _, x := range f.Calls() {
		if x == c {
			n++
		}
	}
	return n
}

func (f *fakeClient) Connect() error {
	f.record("Connect")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.connErrs) > 0 {
		err := f.connErrs[0]
		f.connErrs = f.connErrs[1:]
		return err
	}
	return nil
}
func (f *fakeClient) PNForLID(_ context.Context, lid types.JID) (types.JID, bool) {
	pn, ok := f.lids[lid]
	return pn, ok
}
func (f *fakeClient) Disconnect() { f.record("Disconnect") }
func (f *fakeClient) Logout(context.Context) error {
	f.record("Logout")
	if f.logoutErr != nil {
		return f.logoutErr
	}
	f.mu.Lock()
	f.account = ""
	f.mu.Unlock()
	return nil
}
func (f *fakeClient) Forget(context.Context) error {
	f.record("Forget")
	f.mu.Lock()
	f.account = ""
	f.mu.Unlock()
	return nil
}
func (f *fakeClient) QRChannel(context.Context) (<-chan QRItem, error) {
	f.record("QRChannel")
	f.qr = make(chan QRItem, 4)
	return f.qr, nil
}
func (f *fakeClient) OnEvent(h func(any)) { f.handler = h }
func (f *fakeClient) Account() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.account
}
func (f *fakeClient) PushName() string { return f.pushName }
func (f *fakeClient) GroupName(_ context.Context, jid string) (string, error) {
	f.record("GroupName " + jid)
	if n, ok := f.groups[jid]; ok {
		return n, nil
	}
	return "", fmt.Errorf("no group %s", jid)
}
func (f *fakeClient) PresenceUnavailable(context.Context) error {
	f.record("PresenceUnavailable")
	return nil
}
func (f *fakeClient) ParseWebMessage(chat types.JID, m *waWeb.WebMessageInfo) (*events.Message, error) {
	return evt(chat, ana, m.GetKey().GetID(), m.GetMessage()), nil
}
func (f *fakeClient) emit(e any) { f.handler(e) }
