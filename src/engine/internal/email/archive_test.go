package email

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

var ctx = context.Background()

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func openArchive(t *testing.T) (*Archive, *clock, string) {
	t.Helper()
	c := &clock{t: t0}
	path := filepath.Join(t.TempDir(), "email.db")
	a, err := OpenArchive(path, c.now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a, c, path
}

// rawMail builds a small email; refs is the References header ("" for none), id "" for no Message-ID.
func rawMail(from, id, refs, subject, body string) []byte {
	h := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n", from, me, subject)
	if id != "" {
		h += "Message-ID: <" + id + ">\r\n"
	}
	if refs != "" {
		h += "References: " + refs + "\r\n"
	}
	return []byte(h + "Content-Type: text/plain; charset=utf-8\r\n\r\n" + body + "\r\n")
}

func TestSameEmailStoredOnce(t *testing.T) {
	a, _, _ := openArchive(t)
	raw := rawMail("Ana Silva <ana@example.com>", "root@x", "", "Lease", "hi")
	for range 2 {
		if err := a.Save(ctx, me, 7, t0, raw); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := a.Count(ctx, me); n != 1 {
		t.Fatalf("%d rows", n)
	}
	// the same message in another account's archive is that account's copy
	if err := a.Save(ctx, "other@gmail.com", 3, t0, raw); err != nil {
		t.Fatal(err)
	}
	if n, _ := a.Count(ctx, "other@gmail.com"); n != 1 {
		t.Fatalf("other account: %d rows", n)
	}
}

func TestThreadsNewestFirstWithMe(t *testing.T) {
	a, _, _ := openArchive(t)
	a.Save(ctx, me, 1, t0, rawMail("Ana Silva <ana@example.com>", "root@x", "", "Lease renewal", "the lease ends"))
	a.Save(ctx, me, 2, t0.Add(time.Hour), rawMail("Booking <no@booking.example>", "b@x", "", "Reservation", "confirmed"))
	a.Save(ctx, me, 3, t0.Add(2*time.Hour), rawMail(me, "r1@x", "<root@x>", "Re: Lease renewal", "can we move it"))
	a.Save(ctx, me, 4, t0.Add(3*time.Hour), fixture(t, "attachments.eml")) // unrelated thread, with files
	reply := rawMail("Ana Silva <ana@example.com>", "r2@x", "<root@x> <r1@x>", "Re: Lease renewal", "fine, see attached")
	a.Save(ctx, me, 5, t0.Add(4*time.Hour), reply)
	threads, err := a.Threads(ctx, me)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 3 {
		t.Fatalf("%d threads: %+v", len(threads), threads)
	}
	lease := threads[0]
	if lease.Thread != "root@x" || lease.Subject != "Lease renewal" || lease.Count != 3 ||
		!slices.Equal(lease.Participants, []string{"Ana Silva", "me"}) || lease.LastSnippet != "fine, see attached" ||
		lease.LastDirection != "in" || lease.LastAt != t0.Add(4*time.Hour).Format(time.RFC3339) || lease.HasAttachments {
		t.Fatalf("lease %+v", lease)
	}
	if threads[1].Thread != "files@example.com" || !threads[1].HasAttachments || threads[2].Thread != "b@x" {
		t.Fatalf("order %+v", threads)
	}
}

func TestThreadOldestFirst(t *testing.T) {
	a, _, _ := openArchive(t)
	a.Save(ctx, me, 2, t0.Add(time.Hour), rawMail(me, "r1@x", "<root@x>", "Re: Lease", "second"))
	a.Save(ctx, me, 1, t0, rawMail("Ana Silva <ana@example.com>", "root@x", "", "Lease", "first"))
	a.Save(ctx, me, 3, t0.Add(2*time.Hour), fixture(t, "attachments.eml"))
	emails, err := a.Thread(ctx, me, "root@x")
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) != 2 || emails[0].Text != "first\r\n" && emails[0].Text != "first\n" || emails[1].Direction != "out" ||
		emails[0].Direction != "in" || emails[0].FromName != "Ana Silva" || !slices.Equal(emails[0].To, []string{me}) ||
		emails[0].Date != t0.Format(time.RFC3339) {
		t.Fatalf("%+v", emails)
	}
	files, _ := a.Thread(ctx, me, "files@example.com")
	if len(files) != 1 || len(files[0].Attachments) != 4 || files[0].Attachments[1].Filename != "Contrato é.pdf" {
		t.Fatalf("%+v", files)
	}
	raw, err := a.Raw(ctx, me, files[0].ID)
	if err != nil || string(raw) != string(fixture(t, "attachments.eml")) {
		t.Fatalf("raw: %v", err)
	}
	if _, err := a.Raw(ctx, "other@gmail.com", files[0].ID); err == nil {
		t.Fatal("another account's email must not be readable")
	}
}

func TestNoMessageIDThreadsAlone(t *testing.T) {
	a, _, _ := openArchive(t)
	a.Save(ctx, me, 1, t0, rawMail("a@example.com", "", "", "One", "one"))
	a.Save(ctx, me, 2, t0.Add(time.Minute), rawMail("a@example.com", "", "", "Two", "two"))
	threads, _ := a.Threads(ctx, me)
	if len(threads) != 2 || threads[0].Thread == "" || threads[0].Thread == threads[1].Thread {
		t.Fatalf("%+v", threads)
	}
	if emails, _ := a.Thread(ctx, me, threads[0].Thread); len(emails) != 1 {
		t.Fatalf("%+v", emails)
	}
}

func TestPurgeByEmailDate(t *testing.T) {
	a, _, _ := openArchive(t)
	a.Save(ctx, me, 1, t0.AddDate(0, 0, -400), fixture(t, "attachments.eml"))
	a.Save(ctx, me, 2, t0.AddDate(0, 0, -10), rawMail("a@example.com", "new@x", "", "New", "new"))
	if err := a.Purge(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if n, _ := a.Count(ctx, me); n != 2 {
		t.Fatalf("forever purged: %d", n)
	}
	if err := a.Purge(ctx, 365); err != nil {
		t.Fatal(err)
	}
	threads, _ := a.Threads(ctx, me)
	if len(threads) != 1 || threads[0].Thread != "new@x" {
		t.Fatalf("%+v", threads)
	}
	var orphans int
	a.db.QueryRow(`SELECT COUNT(*) FROM attachments`).Scan(&orphans)
	if orphans != 0 {
		t.Fatalf("%d attachment rows left behind", orphans)
	}
}

func TestForgetKeepsOrDeletes(t *testing.T) {
	a, _, _ := openArchive(t)
	a.PutAccount(ctx, Account{Address: me, State: "connected"})
	a.Save(ctx, me, 1, t0, rawMail("a@example.com", "x@x", "", "Kept", "kept"))
	if err := a.Forget(ctx, me, false); err != nil {
		t.Fatal(err)
	}
	acct, _ := a.Current(ctx)
	if acct == nil || acct.State != "disconnected" {
		t.Fatalf("%+v", acct)
	}
	if n, _ := a.Count(ctx, me); n != 1 {
		t.Fatalf("emails not kept: %d", n)
	}
	if err := a.Forget(ctx, me, true); err != nil {
		t.Fatal(err)
	}
	if acct, _ := a.Current(ctx); acct != nil {
		t.Fatalf("account left: %+v", acct)
	}
	if n, _ := a.Count(ctx, me); n != 0 {
		t.Fatalf("emails not deleted: %d", n)
	}
}

func TestCursorSurvivesReopen(t *testing.T) {
	a, c, path := openArchive(t)
	a.PutAccount(ctx, Account{Address: me, State: "copying", CopyOnMobile: true})
	if err := a.SetCursor(ctx, me, Cursor{UIDValidity: 42, LastUID: 3000, CopiedUID: 1000, CopyUntilUID: 2000}); err != nil {
		t.Fatal(err)
	}
	if err := a.SetState(ctx, me, "auth_failed"); err != nil {
		t.Fatal(err)
	}
	a.Close()
	b, err := OpenArchive(path, c.now)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	acct, _ := b.Current(ctx)
	if acct == nil || *acct != (Account{Address: me, Cursor: Cursor{UIDValidity: 42, LastUID: 3000, CopiedUID: 1000, CopyUntilUID: 2000}, CopyOnMobile: true, State: "auth_failed"}) {
		t.Fatalf("%+v", acct)
	}
}

func TestCurrentIsMostRecent(t *testing.T) {
	a, c, _ := openArchive(t)
	if acct, err := a.Current(ctx); acct != nil || err != nil {
		t.Fatalf("empty archive: %+v %v", acct, err)
	}
	a.PutAccount(ctx, Account{Address: "old@gmail.com", State: "disconnected"})
	c.t = c.t.Add(time.Hour)
	a.PutAccount(ctx, Account{Address: me, State: "connected"})
	if acct, _ := a.Current(ctx); acct.Address != me {
		t.Fatalf("%+v", acct)
	}
}

func TestAccountByAddress(t *testing.T) {
	a, c, _ := openArchive(t)
	a.PutAccount(ctx, Account{Address: me, Cursor: Cursor{UIDValidity: 9, LastUID: 50}, State: "disconnected"})
	c.t = c.t.Add(time.Hour)
	a.PutAccount(ctx, Account{Address: "other@gmail.com", State: "connected"})
	if acct, err := a.Account(ctx, me); err != nil || acct == nil || acct.LastUID != 50 {
		t.Fatalf("%+v %v", acct, err)
	}
	if acct, err := a.Account(ctx, "nobody@gmail.com"); acct != nil || err != nil {
		t.Fatalf("%+v %v", acct, err)
	}
}

func TestUnparseableStoredAnyway(t *testing.T) {
	a, _, _ := openArchive(t)
	raw := []byte("this is not a header\r\n\r\nbody")
	if err := a.Save(ctx, me, 1, t0, raw); err != nil {
		t.Fatal(err)
	}
	threads, _ := a.Threads(ctx, me)
	if len(threads) != 1 {
		t.Fatalf("%+v", threads)
	}
}

func TestReplyTargetOfIncomingEmail(t *testing.T) {
	a, _, _ := openArchive(t)
	a.Save(ctx, me, 1, t0, rawMail(me, "root@x", "", "Hello", "first"))
	a.Save(ctx, me, 2, t0.Add(time.Hour), rawMail("Ana Silva <ana@example.com>", "r1@x", "<root@x>", "Re: Hello", "hi"))
	got, err := a.ReplyTarget(ctx, me, "root@x")
	if err != nil {
		t.Fatal(err)
	}
	if got != (Outgoing{To: "ana@example.com", Subject: "Re: Hello", InReplyTo: "r1@x"}) {
		t.Fatalf("%+v", got)
	}
	if _, err := a.ReplyTarget(ctx, me, "nope@x"); !errors.Is(err, errNotFound) {
		t.Fatalf("unknown thread: %v", err)
	}
}

func TestReplyTargetOfOwnEmailIsItsRecipient(t *testing.T) {
	a, _, _ := openArchive(t)
	out := []byte("From: " + me + "\r\nTo: Bob <b@y.com>\r\nSubject: Re: x\r\nMessage-ID: <o1@x>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nsent\r\n")
	a.Save(ctx, me, 1, t0, out)
	got, err := a.ReplyTarget(ctx, me, "o1@x")
	if err != nil {
		t.Fatal(err)
	}
	if got != (Outgoing{To: "b@y.com", Subject: "Re: x", InReplyTo: "o1@x"}) {
		t.Fatalf("%+v", got)
	}
}

func TestReplyTargetPrefersReplyTo(t *testing.T) {
	a, _, _ := openArchive(t)
	raw := []byte("From: Booking <no-reply@booking.example>\r\nReply-To: Desk <desk@booking.example>\r\nTo: " + me +
		"\r\nSubject: Your stay\r\nMessage-ID: <b1@x>\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nhi\r\n")
	a.Save(ctx, me, 1, t0, raw)
	got, err := a.ReplyTarget(ctx, me, "b1@x")
	if err != nil || got.To != "desk@booking.example" {
		t.Fatalf("%+v %v", got, err)
	}
}
