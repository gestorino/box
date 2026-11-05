package sms

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var ctx = context.Background()

var t0 = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func received(path, number, text string, at time.Time) ModemSMS {
	return ModemSMS{Path: path, Number: number, Text: text, State: "received", Timestamp: at.Format(time.RFC3339)}
}

type rig struct {
	m       *fakeModem
	a       *Archive
	r       *Archiver
	c       *clock
	path    string
	keepFor int
	known   bool // hostd has answered
}

func newRig(t *testing.T) *rig {
	t.Helper()
	g := &rig{m: &fakeModem{instance: ":1.10"}, c: &clock{t: t0}, keepFor: 365, known: true}
	g.path = filepath.Join(t.TempDir(), "sms.db")
	a, err := OpenArchive(g.path, g.c.now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	g.a = a
	g.r = NewArchiver(g.m, a, func() (int, bool) { return g.keepFor, g.known }, g.c.now)
	return g
}

func TestSMSStoredThenDeleted(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "+351911111111", "hi", t0)}
	g.r.Tick(ctx)
	if len(g.m.deleted) != 1 || len(g.m.msgs) != 0 {
		t.Fatalf("deleted %v left %v", g.m.deleted, g.m.msgs)
	}
	th, _ := g.a.Thread(ctx, "+351911111111")
	if len(th) != 1 || th[0].Text != "hi" || th[0].Direction != "in" {
		t.Fatalf("thread %+v", th)
	}
}

func TestStoreFailureNeverDeletes(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "+351911111111", "keep me", t0)}
	g.a.Close() // the database is gone
	g.r.Tick(ctx)
	if len(g.m.deleted) != 0 || len(g.m.msgs) != 1 {
		t.Fatalf("deleted %v — a message not in the database must stay on the modem", g.m.deleted)
	}
}

func TestSMSSeenTwiceStoredOnce(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "+351911111111", "hi", t0)}
	g.m.deleteErr = errors.New("modem busy")
	g.r.Tick(ctx)
	// ModemManager restarts: the same message comes back under a new path and a new instance.
	g.m.deleteErr = nil
	g.m.instance = ":1.42"
	g.m.msgs = []ModemSMS{received("/SMS/9", "+351911111111", "hi", t0)}
	g.r.Tick(ctx)
	th, _ := g.a.Thread(ctx, "+351911111111")
	if len(th) != 1 || len(g.m.msgs) != 0 {
		t.Fatalf("thread %+v, left on modem %v", th, g.m.msgs)
	}
}

func TestDeleteTargetsTheListedInstance(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "+351911111111", "hi", t0)}
	g.m.onList = func() { g.m.mu.Lock(); g.m.instance = ":1.99"; g.m.mu.Unlock() } // restarted after the list
	g.r.Tick(ctx)
	if len(g.m.deleted) != 0 {
		t.Fatalf("deleted %v on a restarted ModemManager", g.m.deleted)
	}
	th, _ := g.a.Thread(ctx, "+351911111111")
	if len(th) != 1 {
		t.Fatalf("the message must still be stored: %+v", th)
	}
	g.m.onList = nil
	g.r.Tick(ctx) // next pass, same instance throughout
	if len(g.m.deleted) != 1 {
		t.Fatalf("not deleted on the next pass: %v", g.m.deleted)
	}
}

func TestReceivingWaits24h(t *testing.T) {
	g := newRig(t)
	part := ModemSMS{Path: "/SMS/3", Number: "+351911111111", Text: "first half", State: "receiving", Timestamp: t0.Format(time.RFC3339)}
	g.m.msgs = []ModemSMS{part}
	g.r.Tick(ctx)
	g.c.advance(23*time.Hour + 59*time.Minute)
	g.r.Tick(ctx)
	if len(g.m.deleted) != 0 {
		t.Fatal("a multipart message in progress was taken too early")
	}
	g.c.advance(time.Minute)
	g.r.Tick(ctx)
	th, _ := g.a.Thread(ctx, "+351911111111")
	if len(g.m.deleted) != 1 || len(th) != 1 || !th[0].Incomplete {
		t.Fatalf("deleted %v thread %+v", g.m.deleted, th)
	}
}

func TestSentStoredAsOutgoing(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{{Path: "/SMS/4", Number: "+351911111111", Text: "on my way", State: "sent"}}
	g.r.Tick(ctx)
	th, _ := g.a.Thread(ctx, "+351911111111")
	if len(th) != 1 || th[0].Direction != "out" || th[0].SentAt != t0.Format(time.RFC3339) {
		t.Fatalf("%+v (a sent SMS without a timestamp is stamped when stored)", th)
	}
	g.m.msgs = []ModemSMS{{Path: "/SMS/5", Number: "+351911111111", Text: "draft", State: "stored"}}
	g.r.Tick(ctx)
	if len(g.m.deleted) != 1 {
		t.Fatalf("an unsent draft must be left alone: %v", g.m.deleted)
	}
}

func TestDataSMSKeepsBytes(t *testing.T) {
	g := newRig(t)
	m := received("/SMS/6", "+351911111111", "", t0)
	m.Data = []byte{1, 2, 3, 4, 5}
	g.m.msgs = []ModemSMS{m}
	g.r.Tick(ctx)
	th, _ := g.a.Thread(ctx, "+351911111111")
	conv, _ := g.a.Conversations(ctx)
	if len(th) != 1 || th[0].DataBytes != 5 || !conv[0].LastIsData {
		t.Fatalf("thread %+v conv %+v", th, conv)
	}
}

func TestConversationsNewestFirstBothDirections(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{
		received("/SMS/1", "+351911111111", "ana 1", t0),
		received("/SMS/2", "MEO", "saldo", t0.Add(time.Minute)),
		{Path: "/SMS/3", Number: "+351911111111", Text: "reply to ana", State: "sent", Timestamp: t0.Add(2 * time.Minute).Format(time.RFC3339)},
	}
	g.r.Tick(ctx)
	conv, _ := g.a.Conversations(ctx)
	if len(conv) != 2 || conv[0].Number != "+351911111111" || conv[0].Count != 2 || conv[0].LastText != "reply to ana" ||
		conv[0].LastDirection != "out" || conv[1].Number != "MEO" {
		t.Fatalf("%+v", conv)
	}
}

func TestThreadOldestFirst(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/2", "X", "second", t0.Add(time.Hour)), received("/SMS/1", "X", "first", t0)}
	g.r.Tick(ctx)
	th, _ := g.a.Thread(ctx, "X")
	if len(th) != 2 || th[0].Text != "first" || th[1].Text != "second" {
		t.Fatalf("%+v", th)
	}
}

func TestPurgeUsesStoredAt(t *testing.T) {
	g := newRig(t)
	g.keepFor = 30
	old := received("/SMS/1", "+351911111111", "from the SIM's past life", t0.AddDate(-2, 0, 0))
	g.m.msgs = []ModemSMS{old}
	g.r.Tick(ctx)
	if th, _ := g.a.Thread(ctx, "+351911111111"); len(th) != 1 {
		t.Fatalf("an old message stored now must be kept: %+v", th)
	}
	g.c.advance(31 * 24 * time.Hour)
	g.r.Tick(ctx)
	if th, _ := g.a.Thread(ctx, "+351911111111"); len(th) != 0 {
		t.Fatalf("not purged after 31 days: %+v", th)
	}
}

func TestRetentionChangePurgesAtOnce(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "X", "a", t0)}
	g.r.Tick(ctx)
	g.c.advance(40 * 24 * time.Hour)
	g.r.Tick(ctx) // hourly purge with 365 days: kept
	g.keepFor = 30
	g.c.advance(time.Minute) // well inside the hour
	g.r.Tick(ctx)
	if th, _ := g.a.Thread(ctx, "X"); len(th) != 0 {
		t.Fatalf("shortening retention must purge at once: %+v", th)
	}
}

func TestRecentIsIncomingByStoredTime(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "WhatsApp", "code 123-456", t0.AddDate(0, 0, -3)),
		{Path: "/SMS/2", Number: "X", Text: "sent", State: "sent"}}
	g.r.Tick(ctx)
	recent, _ := g.a.Recent(ctx, 10*time.Minute)
	if len(recent) != 1 || recent[0].Text != "code 123-456" {
		t.Fatalf("%+v", recent)
	}
}

func TestSurvivesReopen(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "X", "kept", t0)}
	g.r.Tick(ctx)
	g.a.Close()
	b, err := OpenArchive(g.path, g.c.now)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if th, _ := b.Thread(ctx, "X"); len(th) != 1 {
		t.Fatalf("%+v", th)
	}
}

func TestNoPurgeUntilRetentionKnown(t *testing.T) {
	g := newRig(t)
	g.m.msgs = []ModemSMS{received("/SMS/1", "X", "kept forever", t0)}
	g.r.Tick(ctx)
	// smsd restarts 400 days later on a box set to keep SMS forever; hostd has not answered yet.
	g.c.advance(400 * 24 * time.Hour)
	g.r = NewArchiver(g.m, g.a, func() (int, bool) { return g.keepFor, g.known }, g.c.now)
	g.known = false
	g.r.Tick(ctx)
	if th, _ := g.a.Thread(ctx, "X"); len(th) != 1 {
		t.Fatalf("purged before the retention was known: %+v", th)
	}
	g.known, g.keepFor = true, 0
	g.r.Tick(ctx)
	if th, _ := g.a.Thread(ctx, "X"); len(th) != 1 {
		t.Fatalf("forever must keep it: %+v", th)
	}
	g.keepFor = 30
	g.r.Tick(ctx)
	if th, _ := g.a.Thread(ctx, "X"); len(th) != 0 {
		t.Fatalf("30 days must purge it: %+v", th)
	}
}

func TestSentWithoutTimestampStoredOnceAcrossRetries(t *testing.T) {
	g := newRig(t)
	sent := ModemSMS{Path: "/SMS/4", Number: "+351911111111", Text: "ok", State: "sent", Reference: 7}
	g.m.msgs = []ModemSMS{sent}
	g.m.deleteErr = errors.New("modem busy")
	g.r.Tick(ctx)
	// ten seconds later ModemManager has restarted: the same message, a new path and instance
	g.c.advance(10 * time.Second)
	g.m.deleteErr = nil
	g.m.instance = ":1.42"
	sent.Path = "/SMS/9"
	g.m.msgs = []ModemSMS{sent}
	g.r.Tick(ctx)
	th, _ := g.a.Thread(ctx, "+351911111111")
	if len(th) != 1 || th[0].SentAt != t0.Format(time.RFC3339) || len(g.m.msgs) != 0 {
		t.Fatalf("thread %+v, left on modem %v", th, g.m.msgs)
	}
	// the same words sent again are another message: another message reference
	g.m.msgs = []ModemSMS{{Path: "/SMS/10", Number: "+351911111111", Text: "ok", State: "sent", Reference: 8}}
	g.r.Tick(ctx)
	if th, _ := g.a.Thread(ctx, "+351911111111"); len(th) != 2 {
		t.Fatalf("a second \"ok\" was lost: %+v", th)
	}
}

func TestEmptyIncompleteStaysOnTheModem(t *testing.T) {
	g := newRig(t)
	// ModemManager assembles text only when the last part arrives: an orphaned part shows nothing.
	g.m.msgs = []ModemSMS{{Path: "/SMS/3", State: "receiving"}}
	g.r.Tick(ctx)
	g.c.advance(25 * time.Hour)
	g.r.Tick(ctx)
	conv, _ := g.a.Conversations(ctx)
	if len(g.m.deleted) != 0 || len(conv) != 0 {
		t.Fatalf("deleted %v, stored %+v — parts we cannot read must stay on the modem", g.m.deleted, conv)
	}
}
