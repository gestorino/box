package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Gotd is the Client over gotd/td. It is thin on purpose and verified live on the box; the logic
// it feeds is tested with a fake.
type Gotd struct {
	ss      *SessionStore
	us      *UpdateState
	version string // the box's release, sent as the app's version

	mu       sync.Mutex
	client   *telegram.Client // the current Run's, nil between runs
	loggedIn qrlogin.LoggedIn
	signedIn chan int64 // Proceed → the waiting Run: the account is signed in and stored
	pending  int64      // signed in by QR or Password, not yet Proceed-ed
	ready    chan struct{}
}

func NewGotd(ss *SessionStore, us *UpdateState, version string) *Gotd {
	return &Gotd{ss: ss, us: us, version: version, ready: make(chan struct{})}
}

const reconnectDelay = 10 * time.Second

func (g *Gotd) options(storage telegram.SessionStorage, h telegram.UpdateHandler, mw ...telegram.Middleware) telegram.Options {
	return telegram.Options{
		// How Telegram lists the box under Devices (with the app's title from my.telegram.org);
		// unset, gotd sends its own version and the Go version as the model.
		Device:         telegram.DeviceConfig{DeviceModel: "Concierge", SystemVersion: "Linux", AppVersion: g.version},
		SessionStorage: storage,
		UpdateHandler:  h,
		Middlewares:    append([]telegram.Middleware{telegram.MiddlewareFunc(floodWait)}, mw...),
		// No Logger: gotd's logs would carry message details.
	}
}

// endOn401 ends the run on any 401 while linked: gotd itself only logs one from its idle
// getDifference or a reconnect's Self, so an ended session would otherwise go unnoticed.
func endOn401(linked func() bool, end func(error)) telegram.MiddlewareFunc {
	return func(next tg.Invoker) telegram.InvokeFunc {
		return func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
			err := next.Invoke(ctx, in, out)
			if auth.IsUnauthorized(err) && linked() {
				end(err)
			}
			return err
		}
	}
}

// sessionGone: gotd's auth status reads a 401 as "not signed in"; with a linked session that means
// Telegram ended it (removed under Devices, or expired) while the box was off.
func sessionGone(authorized, linked bool) error {
	if !authorized && linked {
		return tgerr.New(401, "SESSION_REVOKED")
	}
	return nil
}

// floodWait sleeps the time Telegram asks for on FLOOD_WAIT and retries.
func floodWait(next tg.Invoker) telegram.InvokeFunc {
	return func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		for {
			err := next.Invoke(ctx, in, out)
			d, ok := tgerr.AsFloodWait(err)
			if !ok {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d + time.Second):
			}
		}
	}
}

func (g *Gotd) Check(ctx context.Context, c Creds) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := telegram.NewClient(c.ID, c.Hash, g.options(&session.StorageMemory{}, nil))
	err := client.Run(ctx, func(ctx context.Context) error {
		_, err := client.QR().Export(ctx) // needs the api_id/api_hash; the token is discarded
		return err
	})
	if tgerr.Is(err, "API_ID_INVALID", "API_ID_PUBLISHED_FLOOD") {
		return ErrCredentials
	}
	return err
}

func (g *Gotd) Run(ctx context.Context, c Creds, events func(any)) error {
	for {
		err := g.runOnce(ctx, c, events)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if auth.IsUnauthorized(err) {
			return err
		}
		slog.Warn("telegram: connection lost, reconnecting", "err", err, "in", reconnectDelay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectDelay):
		}
	}
}

func (g *Gotd) runOnce(ctx context.Context, c Creds, events func(any)) error {
	d := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(d)
	dispatch(d, events)
	mgr := updates.New(updates.Config{
		Handler:          d,
		Storage:          g.us,
		AccessHasher:     g.us,
		UserAccessHasher: g.us,
		OnTooLong:        func() { events(TooLong{}) },
		OnChannelTooLong: func(id int64) { events(TooLong{ChannelID: id}) },
	})
	ctx, end := context.WithCancelCause(ctx)
	defer end(nil)
	client := telegram.NewClient(c.ID, c.Hash, g.options(g.ss, mgr, endOn401(g.ss.Linked, end)))
	err := client.Run(ctx, func(ctx context.Context) error {
		signedIn := make(chan int64, 1)
		g.mu.Lock()
		g.client, g.loggedIn, g.signedIn = client, loggedIn, signedIn
		close(g.ready)
		g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			g.client, g.ready = nil, make(chan struct{})
			g.mu.Unlock()
		}()

		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if err := sessionGone(status.Authorized, g.ss.Linked()); err != nil {
			return err
		}
		var userID int64
		if status.Authorized {
			userID = status.User.ID
		} else {
			select { // signed out: wait for a QR login
			case <-ctx.Done():
				return ctx.Err()
			case userID = <-signedIn:
			}
		}
		return mgr.Run(ctx, client.API(), userID, updates.AuthOptions{
			OnStart: func(context.Context) { events(Connected{}) },
		})
	})
	if cause := context.Cause(ctx); auth.IsUnauthorized(cause) {
		return cause
	}
	return err
}

func dispatch(d tg.UpdateDispatcher, events func(any)) {
	d.OnNewMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		events(NewMessage{Msg: u.Message, Ent: e})
		return nil
	})
	d.OnNewChannelMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		events(NewMessage{Msg: u.Message, Ent: e})
		return nil
	})
	d.OnEditMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateEditMessage) error {
		events(EditMessage{Msg: u.Message, Ent: e})
		return nil
	})
	d.OnEditChannelMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateEditChannelMessage) error {
		events(EditMessage{Msg: u.Message, Ent: e})
		return nil
	})
	d.OnReadHistoryInbox(func(_ context.Context, _ tg.Entities, u *tg.UpdateReadHistoryInbox) error {
		if key := PeerKey(u.Peer); strings.HasPrefix(key, "user:") {
			events(ReadInbox{Peer: key, MaxID: u.MaxID})
		}
		return nil
	})
	d.OnDeleteMessages(func(_ context.Context, _ tg.Entities, u *tg.UpdateDeleteMessages) error {
		events(DeletedCommon{IDs: u.Messages})
		return nil
	})
	d.OnDeleteChannelMessages(func(_ context.Context, _ tg.Entities, u *tg.UpdateDeleteChannelMessages) error {
		events(DeletedChannel{ChannelID: u.ChannelID, IDs: u.Messages})
		return nil
	})
}

// current waits for the running connection.
func (g *Gotd) current(ctx context.Context) (*telegram.Client, error) {
	for {
		g.mu.Lock()
		client, ready := g.client, g.ready
		g.mu.Unlock()
		if client != nil {
			return client, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ready:
		}
	}
}

func (g *Gotd) Proceed() {
	g.mu.Lock()
	ch, id := g.signedIn, g.pending
	g.pending = 0
	g.mu.Unlock()
	if ch == nil || id == 0 {
		return
	}
	select {
	case ch <- id:
	default:
	}
}

func (g *Gotd) QR(ctx context.Context, show func(url string, expires time.Time)) (Self, error) {
	client, err := g.current(ctx)
	if err != nil {
		return Self{}, err
	}
	g.mu.Lock()
	loggedIn := g.loggedIn
	g.mu.Unlock()
	a, err := client.QR().Auth(ctx, loggedIn, func(_ context.Context, t qrlogin.Token) error {
		show(t.URL(), t.Expires())
		return nil
	})
	if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
		return Self{}, ErrPasswordNeeded
	}
	if err != nil {
		return Self{}, err
	}
	return g.authorized(a)
}

func (g *Gotd) Password(ctx context.Context, pw string) (Self, error) {
	client, err := g.current(ctx)
	if err != nil {
		return Self{}, err
	}
	a, err := client.Auth().Password(ctx, pw)
	if errors.Is(err, auth.ErrPasswordInvalid) {
		return Self{}, ErrPassword
	}
	if err != nil {
		return Self{}, err
	}
	return g.authorized(a)
}

func (g *Gotd) authorized(a *tg.AuthAuthorization) (Self, error) {
	u, ok := a.User.(*tg.User)
	if !ok {
		return Self{}, fmt.Errorf("unexpected user %T", a.User)
	}
	g.mu.Lock()
	g.pending = u.ID
	g.mu.Unlock()
	return selfFrom(u), nil
}

func selfFrom(u *tg.User) Self {
	s := Self{ID: u.ID, Name: userName(u)}
	if u.Phone != "" {
		s.Phone = "+" + u.Phone
	}
	return s
}

func (g *Gotd) LogOut(ctx context.Context) error {
	client, err := g.current(ctx)
	if err != nil {
		return err
	}
	_, err = client.API().AuthLogOut(ctx)
	return err
}

func (g *Gotd) Offline(ctx context.Context) error {
	client, err := g.current(ctx)
	if err != nil {
		return err
	}
	_, err = client.API().AccountUpdateStatus(ctx, true)
	return err
}

func (g *Gotd) ReadHistory(ctx context.Context, p InputPeer, maxID int) error {
	client, err := g.current(ctx)
	if err != nil {
		return err
	}
	_, err = client.API().MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: inputPeer(p), MaxID: maxID})
	return err
}

func (g *Gotd) Dialogs(ctx context.Context, page func(Dialog) error) error {
	client, err := g.current(ctx)
	if err != nil {
		return err
	}
	return dialogsOf(ctx, client.API(), page)
}

// dialogsOf walks the main chat list, then the Archive folder (1): muted groups and channels often
// live there, and the main list shows the Archive only as one folder entry.
func dialogsOf(ctx context.Context, api *tg.Client, page func(Dialog) error) error {
	for _, folder := range []int{0, 1} {
		if err := folderDialogs(ctx, api, folder, page); err != nil {
			return err
		}
	}
	return nil
}

func folderDialogs(ctx context.Context, api *tg.Client, folder int, page func(Dialog) error) error {
	return dialogs.NewQueryBuilder(api).GetDialogs().FolderID(folder).BatchSize(100).ForEach(ctx, func(ctx context.Context, e dialogs.Elem) error {
		dl, ok := e.Dialog.(*tg.Dialog)
		if !ok { // a folder
			return nil
		}
		ent := tg.Entities{Users: e.Entities.Users(), Chats: e.Entities.Chats(), Channels: e.Entities.Channels()}
		p, ok := PeerFrom("", PeerKey(dl.Peer), ent)
		if !ok {
			return nil
		}
		return page(Dialog{Peer: p, Pts: dl.Pts, ReadInboxMaxID: dl.ReadInboxMaxID})
	})
}

func (g *Gotd) History(ctx context.Context, p InputPeer, limit int) ([]tg.MessageClass, tg.Entities, error) {
	client, err := g.current(ctx)
	if err != nil {
		return nil, tg.Entities{}, err
	}
	res, err := client.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: inputPeer(p), Limit: limit})
	if err != nil {
		return nil, tg.Entities{}, err
	}
	m, ok := res.AsModified()
	if !ok {
		return nil, tg.Entities{}, nil
	}
	return m.GetMessages(), entitiesFrom(m.GetUsers(), m.GetChats()), nil
}

func (g *Gotd) Send(ctx context.Context, p InputPeer, text string) (int, time.Time, error) {
	client, err := g.current(ctx)
	if err != nil {
		return 0, time.Time{}, err
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return 0, time.Time{}, err
	}
	peer := inputPeer(p)
	if p.Key == "self" {
		peer = &tg.InputPeerSelf{}
	}
	res, err := client.API().MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer: peer, Message: text, RandomID: int64(binary.LittleEndian.Uint64(random[:])),
	})
	if err != nil {
		return 0, time.Time{}, err
	}
	id, at, ok := sentID(res)
	if !ok {
		return 0, time.Time{}, errors.New("telegram: no message id in the send's answer")
	}
	return id, at, nil
}

// sentID finds the sent message's id and time in messages.sendMessage's answer.
func sentID(u tg.UpdatesClass) (int, time.Time, bool) {
	switch u := u.(type) {
	case *tg.UpdateShortSentMessage:
		return u.ID, time.Unix(int64(u.Date), 0), true
	case *tg.Updates:
		for _, up := range u.Updates {
			var m tg.MessageClass
			switch up := up.(type) {
			case *tg.UpdateMessageID:
				return up.ID, time.Unix(int64(u.Date), 0), true
			case *tg.UpdateNewMessage:
				m = up.Message
			case *tg.UpdateNewChannelMessage:
				m = up.Message
			}
			if msg, ok := m.(*tg.Message); ok {
				return msg.ID, time.Unix(int64(msg.Date), 0), true
			}
		}
	}
	return 0, time.Time{}, false
}

func inputPeer(p InputPeer) tg.InputPeerClass {
	kind, id := splitKey(p.Key)
	switch kind {
	case "user":
		return &tg.InputPeerUser{UserID: id, AccessHash: p.AccessHash}
	case "chat":
		return &tg.InputPeerChat{ChatID: id}
	case "channel":
		return &tg.InputPeerChannel{ChannelID: id, AccessHash: p.AccessHash}
	}
	return &tg.InputPeerEmpty{}
}

func entitiesFrom(users []tg.UserClass, chats []tg.ChatClass) tg.Entities {
	ent := tg.Entities{Users: map[int64]*tg.User{}, Chats: map[int64]*tg.Chat{}, Channels: map[int64]*tg.Channel{}}
	for _, u := range users {
		if u, ok := u.(*tg.User); ok {
			ent.Users[u.ID] = u
		}
	}
	for _, c := range chats {
		switch c := c.(type) {
		case *tg.Chat:
			ent.Chats[c.ID] = c
		case *tg.Channel:
			ent.Channels[c.ID] = c
		}
	}
	return ent
}
