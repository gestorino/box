package sms

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrationLeavesOldMessagesRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sms.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0] + `PRAGMA user_version = 1;
		INSERT INTO sms (direction, number, text, pdu_type, incomplete, sent_at, stored_at, dedupe)
		VALUES ('in', '+351911111111', 'old', 'deliver', 0, '2026-10-28T12:00:00Z', '2026-10-28T12:00:00Z', 'x');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	a, err := OpenArchive(path, func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if n, err := a.Unread(ctx); err != nil || n != 0 {
		t.Fatalf("unread %d, %v", n, err)
	}
	if c, _ := a.Conversations(ctx); len(c) != 1 || c[0].Unread != 0 {
		t.Fatalf("conversations %+v", c)
	}
}

func TestIncomingCountsOutgoingDoesNot(t *testing.T) {
	g := newRig(t)
	g.a.Save(ctx, received("/SMS/1", "+351911111111", "hi", t0), false)
	g.a.Save(ctx, ModemSMS{Path: "/SMS/2", Number: "+351911111111", Text: "yo", State: "sent", Reference: 1}, false)
	if n, err := g.a.Unread(ctx); err != nil || n != 1 {
		t.Fatalf("unread %d, %v", n, err)
	}
	if c, _ := g.a.Conversations(ctx); len(c) != 1 || c[0].Unread != 1 {
		t.Fatalf("conversations %+v", c)
	}
}

func TestSavingTwiceCountsOnce(t *testing.T) {
	g := newRig(t)
	m := received("/SMS/1", "+351911111111", "hi", t0)
	g.a.Save(ctx, m, false)
	g.a.Save(ctx, m, false)
	if n, _ := g.a.Unread(ctx); n != 1 {
		t.Fatalf("unread %d", n)
	}
}

func TestMarkReadClearsOneNumberOnly(t *testing.T) {
	g := newRig(t)
	g.a.Save(ctx, received("/SMS/1", "+351911111111", "a", t0), false)
	g.a.Save(ctx, received("/SMS/2", "+351922222222", "b", t0), false)
	if err := g.a.MarkRead(ctx, "+351911111111"); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.a.Unread(ctx); n != 1 {
		t.Fatalf("unread %d", n)
	}
}

func TestUnknownSenderCanBeMarkedRead(t *testing.T) {
	g := newRig(t)
	g.a.Save(ctx, received("/SMS/1", "", "a", t0), false)
	if n, _ := g.a.Unread(ctx); n != 1 {
		t.Fatalf("unread %d", n)
	}
	if err := g.a.MarkRead(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.a.Unread(ctx); n != 0 {
		t.Fatalf("unread %d", n)
	}
}

func TestReadRequiresNumber(t *testing.T) {
	g := newRig(t)
	h := NewHandler(g.a, g.m, g.r.Tick, g.ls())
	if rec := post(h, "/sms/read", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("%d", rec.Code)
	}
	g.a.Save(ctx, received("/SMS/1", "+351911111111", "a", t0), false)
	if rec := post(h, "/sms/read", `{"number":"+351911111111"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("%d", rec.Code)
	}
	rec := get(h, "/sms/unread")
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"count\":0}\n" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
