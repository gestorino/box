package email

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// allMailName is localised on purpose: the adapter must find the mailbox by its \All flag.
const allMailName = "[Gmail]/Todo o correio"

// gmailLike answers LIST as Gmail does, flagging All Mail \All (the in-memory server cannot set
// special-use flags itself).
type gmailLike struct{ imapserver.Session }

func (gmailLike) List(w *imapserver.ListWriter, _ string, _ []string, _ *imap.ListOptions) error {
	for _, l := range []imap.ListData{
		{Mailbox: "INBOX", Delim: '/'},
		{Mailbox: allMailName, Delim: '/', Attrs: []imap.MailboxAttr{imap.MailboxAttrAll}},
	} {
		if err := w.WriteList(&l); err != nil {
			return err
		}
	}
	return nil
}

// loopbackIMAP serves an in-memory mailbox on 127.0.0.1 — a real IMAP conversation, no network.
func loopbackIMAP(t *testing.T) (string, *imapmemserver.User) {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(me, "app-password")
	for _, name := range []string{"INBOX", allMailName} {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return gmailLike{mem.NewSession()}, nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapSpecialUse: {}, imap.CapListExtended: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String(), user
}

func appendMail(t *testing.T, user *imapmemserver.User, raw []byte, flags ...imap.Flag) {
	t.Helper()
	if _, err := user.Append(allMailName, bytes.NewReader(raw), &imap.AppendOptions{Flags: flags, Time: t0}); err != nil {
		t.Fatal(err)
	}
}

func TestIMAPReadOnlyAgainstMemServer(t *testing.T) {
	addr, user := loopbackIMAP(t)
	raws := [][]byte{fixture(t, "plain.eml"), fixture(t, "attachments.eml"), rawMail(me, "draft@x", "", "Draft", "unsent")}
	appendMail(t, user, raws[0])
	appendMail(t, user, raws[1])
	appendMail(t, user, raws[2], imap.FlagDraft)

	sess, err := NewIMAPDial(addr, false)(ctx, me, "app-password")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.UIDValidity() == 0 || sess.UIDNext() != 4 {
		t.Fatalf("uidvalidity %d uidnext %d", sess.UIDValidity(), sess.UIDNext())
	}
	uids, err := sess.UIDsSince(ctx, time.Time{})
	if err != nil || len(uids) != 3 {
		t.Fatalf("%v %v", uids, err)
	}
	if after, err := sess.UIDsAfter(ctx, 3); err != nil || len(after) != 0 {
		t.Fatalf("nothing after the last UID, got %v %v", after, err) // "4:*" names the last message in IMAP
	}
	msgs, err := sess.Fetch(ctx, uids)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("%d %v", len(msgs), err)
	}
	for i, m := range msgs {
		if !bytes.Equal(bytes.ReplaceAll(m.Raw, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(raws[i], []byte("\r\n"), []byte("\n"))) {
			t.Fatalf("message %d differs", i)
		}
		if m.Draft != (i == 2) || !m.Date.Equal(t0) {
			t.Fatalf("message %d: draft %v date %v", i, m.Draft, m.Date)
		}
	}
	st, err := user.Status(allMailName, &imap.StatusOptions{NumMessages: true, NumUnseen: true})
	if err != nil || *st.NumMessages != 3 || *st.NumUnseen != 3 {
		t.Fatalf("the mailbox changed: %+v %v", st, err)
	}
}

func TestIMAPRefusedLogin(t *testing.T) {
	addr, _ := loopbackIMAP(t)
	_, err := NewIMAPDial(addr, false)(ctx, me, "wrong-secret")
	if !errors.Is(err, ErrAuth) || strings.Contains(err.Error(), "wrong-secret") {
		t.Fatalf("%v", err)
	}
}

func TestIMAPWaitWakesOnNewMail(t *testing.T) {
	addr, user := loopbackIMAP(t)
	sess, err := NewIMAPDial(addr, false)(ctx, me, "app-password")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	go func() {
		time.Sleep(200 * time.Millisecond)
		appendMail(t, user, fixture(t, "plain.eml"))
	}()
	start := time.Now()
	if err := sess.Wait(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("woke after %v", took)
	}
	if uids, err := sess.UIDsAfter(ctx, 0); err != nil || len(uids) != 1 {
		t.Fatalf("%v %v", uids, err)
	}
}

func TestIMAPCancelledContextEndsWait(t *testing.T) {
	addr, _ := loopbackIMAP(t)
	sess, err := NewIMAPDial(addr, false)(ctx, me, "app-password")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	c, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	sess.Wait(c, time.Minute)
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Wait outlived its context by %v", took)
	}
}

func TestQuietConnEndsSilentReadsOnly(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	qc := &quietConn{Conn: client, quiet: 80 * time.Millisecond}
	defer qc.Close()

	go func() { // a slow but steady stream: a byte every 30 ms, for 300 ms
		for range 10 {
			time.Sleep(30 * time.Millisecond)
			server.Write([]byte{'x'})
		}
	}()
	buf := make([]byte, 1)
	for i := range 10 {
		if _, err := qc.Read(buf); err != nil {
			t.Fatalf("read %d of a moving stream failed: %v", i, err)
		}
	}
	start := time.Now()
	if _, err := qc.Read(buf); err == nil || time.Since(start) > time.Second {
		t.Fatalf("a silent server must end the read: %v after %v", err, time.Since(start))
	}

	client2, server2 := net.Pipe()
	defer server2.Close()
	waiting := &quietConn{Conn: client2, quiet: 80 * time.Millisecond}
	waiting.suspend(true) // IDLE: silence is normal
	done := make(chan error, 1)
	go func() { _, err := waiting.Read(buf); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("a suspended read ended: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	server2.Write([]byte{'x'})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
