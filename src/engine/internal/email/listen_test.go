package email

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	listenv1 "concierge/engine/internal/gen/listen/v1"
	"concierge/engine/internal/gen/listen/v1/listenv1connect"
	"concierge/engine/internal/listen"
	"concierge/engine/internal/listen/listentest"
)

const plusAddr = "owner+a1@example.com"

// confirmMail is an email from a site to a plus-address, with an HTML part and an attachment.
func confirmMail(to, subject string) []byte {
	return []byte("From: Site <noreply@site.example>\r\nTo: " + to + "\r\nSubject: " + subject + "\r\nMessage-ID: <" + subject + "@site>\r\n" +
		"Content-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: multipart/alternative; boundary=inner\r\n\r\n" +
		"--inner\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nplain body\r\n" +
		"--inner\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<a href=\"https://site.example/confirm?t=1\">go</a>\r\n--inner--\r\n" +
		"--outer\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=terms.pdf\r\n\r\n%PDF\r\n--outer--\r\n")
}

// ls is the listen service the handler tests mount.
func (g *srig) ls() listenv1connect.ListenServiceHandler {
	return NewListen(g.s, g.a, listen.NewHub(listen.MatchEmail))
}

// listening opens a listener on the rig's archive, served as emaild serves it, and waits for its
// caught_up so whatever is stored next is live.
func listening(t *testing.T, g *srig, req *listenv1.ListenRequest) <-chan *listenv1.Event {
	t.Helper()
	hub := listen.NewHub(listen.MatchEmail)
	g.a.SetHub(hub)
	srv := listentest.Serve(t, NewListen(g.s, g.a, hub))
	c, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := listen.NewClient(srv.Socket).Listen(c, req)
	waitCaughtUp(t, events)
	return events
}

func waitCaughtUp(t *testing.T, events <-chan *listenv1.Event) {
	t.Helper()
	select {
	case e := <-events:
		if e.GetCaughtUp() == nil {
			t.Fatalf("first event %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no caught_up")
	}
}

func TestAStoredEmailReachesTheListenerForItsAddress(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	events := listening(t, g, &listenv1.ListenRequest{To: plusAddr, From: "noreply@site.example"})
	if err := g.a.Save(ctx, me, 5, t0, confirmMail(plusAddr, "Confirm")); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-events:
		m := e.GetMessage()
		em := m.GetEmail()
		if em.GetSubject() != "Confirm" || em.GetFromAddr() != "noreply@site.example" || len(em.GetTo()) != 1 ||
			!strings.Contains(em.GetHtml(), `href="https://site.example/confirm?t=1"`) || em.GetText() != "plain body" ||
			len(em.GetAttachments()) != 1 || em.GetAttachments()[0].GetFilename() != "terms.pdf" ||
			m.GetId() == 0 || !m.GetStoredAt().AsTime().Equal(t0) {
			t.Fatalf("event %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener got nothing")
	}
}

// A listener gets the HTML as it reads, its transfer encoding undone: the engine finds a link in it
// as the site wrote it.
func TestAListenerGetsTheHTMLDecoded(t *testing.T) {
	a, _, _ := openArchive(t)
	raw := "From: Site <noreply@site.example>\r\nTo: " + plusAddr + "\r\nSubject: Confirm\r\nMessage-ID: <qp@site>\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		`<a class=3D"b" href=3D"https://site.example/confirm?t=3D1&amp;u=3D2">go</a>` + "\r\n"
	if err := a.Save(ctx, me, 1, t0, []byte(raw)); err != nil {
		t.Fatal(err)
	}
	got, err := a.Since(ctx, me, &listenv1.ListenRequest{To: plusAddr, Since: timestamppb.New(time.Time{})})
	if err != nil || len(got) != 1 || !strings.Contains(got[0].GetEmail().GetHtml(), `href="https://site.example/confirm?t=1&amp;u=2"`) {
		t.Fatalf("%v, %v", got, err)
	}
}

func TestAnEmailToAnotherAddressReachesNoListener(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	events := listening(t, g, &listenv1.ListenRequest{To: plusAddr})
	g.a.Save(ctx, me, 5, t0, confirmMail("owner+b2@example.com", "Other"))
	select {
	case e := <-events:
		t.Fatalf("got %v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestARestoredEmailIsPublishedOnce(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	events := listening(t, g, &listenv1.ListenRequest{To: plusAddr})
	raw := confirmMail(plusAddr, "Once")
	g.a.Save(ctx, me, 5, t0, raw)
	g.a.Save(ctx, me, 9, t0, raw) // the mailbox was renumbered: same email, a new UID
	g.a.Save(ctx, me, 10, t0, confirmMail(plusAddr, "Twice"))
	var subjects []string
	for len(subjects) < 2 {
		select {
		case e := <-events:
			subjects = append(subjects, e.GetMessage().GetEmail().GetSubject())
		case <-time.After(5 * time.Second):
			t.Fatalf("got %v", subjects)
		}
	}
	if subjects[0] != "Once" || subjects[1] != "Twice" {
		t.Fatalf("got %v", subjects)
	}
}

func TestAnOutgoingEmailReachesNoListener(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	events := listening(t, g, &listenv1.ListenRequest{To: plusAddr})
	g.a.Save(ctx, me, 5, t0, []byte("From: "+me+"\r\nTo: "+plusAddr+"\r\nSubject: Sent\r\nContent-Type: text/plain\r\n\r\nhi\r\n"))
	select {
	case e := <-events:
		t.Fatalf("got %v", e)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAnEmailListenerNeedsAnAddress(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	srv := listentest.Serve(t, NewListen(g.s, g.a, listen.NewHub(listen.MatchEmail)))
	c, cancel := context.WithCancel(context.Background())
	defer cancel()
	select {
	case e, ok := <-listen.NewClient(srv.Socket).Listen(c, &listenv1.ListenRequest{From: "noreply@site.example"}):
		if ok {
			t.Fatalf("got %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the channel stayed open")
	}
}

func TestNoMailboxIsAFailedPreconditionUntilOneIsConnected(t *testing.T) {
	g := newSrig(t)
	hub := listen.NewHub(listen.MatchEmail)
	g.a.SetHub(hub)
	srv := listentest.Serve(t, NewListen(g.s, g.a, hub))
	c, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := listen.NewClient(srv.Socket).Listen(c, &listenv1.ListenRequest{To: plusAddr})
	select {
	case e := <-events:
		t.Fatalf("opened without a mailbox: %v", e)
	case <-time.After(300 * time.Millisecond):
	}
	g.connect(t, "from_now", false)
	waitCaughtUp(t, events) // the client's retry
	g.a.Save(ctx, me, 5, t0, confirmMail(plusAddr, "Late"))
	select {
	case e := <-events:
		if e.GetMessage().GetEmail().GetSubject() != "Late" {
			t.Fatalf("event %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener got nothing")
	}
}

func TestReplayOfTheWholeArchive(t *testing.T) {
	g := newSrig(t)
	g.connect(t, "from_now", false)
	g.a.Save(ctx, me, 5, t0, confirmMail(plusAddr, "Before"))
	g.a.Save(ctx, me, 6, t0, confirmMail("owner+b2@example.com", "Elsewhere"))
	hub := listen.NewHub(listen.MatchEmail)
	g.a.SetHub(hub)
	srv := listentest.Serve(t, NewListen(g.s, g.a, hub))
	c, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := listen.NewClient(srv.Socket).Listen(c, &listenv1.ListenRequest{To: plusAddr, Since: timestamppb.New(time.Time{})})
	select {
	case e := <-events:
		if e.GetMessage().GetEmail().GetSubject() != "Before" {
			t.Fatalf("event %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no replay")
	}
	waitCaughtUp(t, events)
}

func TestSinceIsComparedInWholeSecondsOnStoredAt(t *testing.T) {
	g := newSrig(t)
	g.c.t = t0.Add(-time.Hour)
	g.a.Save(ctx, me, 1, t0, confirmMail(plusAddr, "Old")) // dated now, stored an hour ago
	g.c.t = t0
	g.a.Save(ctx, me, 2, t0.Add(-time.Hour), confirmMail(plusAddr, "New"))
	for since, want := range map[time.Time]int{t0.Add(-time.Minute): 1, t0.Add(900 * time.Millisecond): 1, t0.Add(time.Second): 0, {}: 2} {
		got, err := g.a.Since(ctx, me, &listenv1.ListenRequest{To: plusAddr, Since: timestamppb.New(since)})
		if err != nil || len(got) != want {
			t.Fatalf("%v: %d, %v", since, len(got), err)
		}
	}
}

// A row whose stored_at doesn't parse is skipped: one bad row never fails the whole replay.
func TestAStoredAtThatDoesNotParseSkipsOnlyItsEmail(t *testing.T) {
	a, _, _ := openArchive(t)
	for uid, subject := range map[uint32]string{1: "Bad", 2: "Good"} {
		if err := a.Save(ctx, me, uid, t0, confirmMail(plusAddr, subject)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.Exec(`UPDATE emails SET stored_at = 'not a time' WHERE uid = 1`); err != nil {
		t.Fatal(err)
	}
	got, err := a.Since(ctx, me, &listenv1.ListenRequest{To: plusAddr, Since: timestamppb.New(time.Time{})})
	if err != nil || len(got) != 1 || got[0].GetEmail().GetSubject() != "Good" {
		t.Fatalf("%v, %v", got, err)
	}
}

func TestStoredAtIsMigratedFromDate(t *testing.T) {
	a, c, path := openArchive(t)
	if err := a.Save(ctx, me, 1, t0, rawMail(ana, "m@x", "", "hi", "hi")); err != nil {
		t.Fatal(err)
	}
	a.Close()
	// as the previous build left it: user_version 2, no stored_at
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`ALTER TABLE accounts ADD COLUMN app_password TEXT NOT NULL DEFAULT ''`, `DROP INDEX emails_stored`, `ALTER TABLE emails DROP COLUMN stored_at`, `PRAGMA user_version = 2`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()
	b, err := OpenArchive(path, c.now)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var date, stored string
	if err := b.db.QueryRow(`SELECT date, stored_at FROM emails`).Scan(&date, &stored); err != nil || stored == "" || stored != date {
		t.Fatalf("%q %q %v", date, stored, err)
	}
}
