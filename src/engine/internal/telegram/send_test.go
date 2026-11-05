package telegram

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func sentCalls(f *fake) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func offlines(f *fake) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.offline
}

func TestSendStoresTheMessageAsMine(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	account := acct(ana.ID)
	if err := s.PutPeer(ctx, Peer{Account: account, Key: "user:42", AccessHash: 9001, Name: "Rui", Kind: "private"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "offline after connect", func() bool { return offlines(f) >= 1 })
	before := offlines(f)
	if err := svc.Send(ctx, "user:42", "hi"); err != nil {
		t.Fatal(err)
	}
	if got := sentCalls(f); !reflect.DeepEqual(got, []string{"user:42 9001 hi"}) {
		t.Fatalf("%v", got)
	}
	msgs, _ := s.Messages(ctx, account, "user:42", 10)
	if len(msgs) != 1 || msgs[0].ID != 77 || !msgs[0].FromMe || msgs[0].Text != "hi" {
		t.Fatalf("%+v", msgs)
	}
	if offlines(f) != before+1 {
		t.Fatalf("offline %d, before %d", offlines(f), before)
	}
}

func TestSendToSavedMessages(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	if err := svc.Send(ctx, "self", "note"); err != nil {
		t.Fatal(err)
	}
	if got := sentCalls(f); !reflect.DeepEqual(got, []string{"self 0 note"}) {
		t.Fatalf("%v", got)
	}
	if msgs, _ := s.Messages(ctx, acct(ana.ID), "user:1000", 10); len(msgs) != 1 || !msgs[0].FromMe {
		t.Fatalf("%+v", msgs)
	}
}

func TestSendToUnknownPeer(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := linked(t, f, s)
	if err := svc.Send(ctx, "user:5", "hi"); !errors.Is(err, errUnknownChat) {
		t.Fatalf("%v", err)
	}
	if len(sentCalls(f)) != 0 {
		t.Fatal("sent")
	}
}

func TestSendWhenNotConnectedIsRefused(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	svc := newService(t, f, s)
	if err := svc.Send(ctx, "self", "hi"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("no credentials: %v", err)
	}
	svc.SetCredentials(ctx, creds)
	waitState(t, svc, "unlinked")
	if err := svc.Send(ctx, "self", "hi"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("unlinked: %v", err)
	}
	if len(sentCalls(f)) != 0 {
		t.Fatal("sent")
	}
}

func TestSentID(t *testing.T) {
	at := int(t0.Unix())
	for name, c := range map[string]struct {
		u    tg.UpdatesClass
		want int
		ok   bool
	}{
		"short":       {&tg.UpdateShortSentMessage{ID: 7, Date: at}, 7, true},
		"message id":  {&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{ID: 8, RandomID: 1}}, Date: at}, 8, true},
		"new message": {&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{ID: 9, Date: at}}}}, 9, true},
		"channel":     {&tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 10, Date: at}}}}, 10, true},
		"nothing":     {&tg.Updates{}, 0, false},
	} {
		id, when, ok := sentID(c.u)
		if id != c.want || ok != c.ok || ok && !when.Equal(time.Unix(int64(at), 0)) {
			t.Errorf("%s: %d %v %t", name, id, when, ok)
		}
	}
}

func TestSendRoute(t *testing.T) {
	s, _ := openTemp(t)
	f := newFake()
	h := NewHandler(linked(t, f, s))
	if rec := call(h, "POST", "/telegram/send", `{"peer":"self","text":"hi"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"peer":"self","text":""}`, `{"peer":"","text":"hi"}`, `{"peer":"self","text":"hi","x":1}`,
		`{"peer":"self","text":"` + strings.Repeat("x", 4097) + `"}`} {
		if rec := call(h, "POST", "/telegram/send", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%.50s: %d", body, rec.Code)
		}
	}
	if rec := call(h, "POST", "/telegram/send", `{"peer":"user:5","text":"hi"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}
	f.with(func(f *fake) { f.sendErr = errors.New("rpc error code 400: PEER_ID_INVALID") })
	if rec := call(h, "POST", "/telegram/send", `{"peer":"self","text":"hi"}`); rec.Code != http.StatusBadGateway ||
		strings.TrimSpace(rec.Body.String()) != `{"error":"message not sent"}` {
		t.Fatalf("send error: %d %s", rec.Code, rec.Body)
	}
}
