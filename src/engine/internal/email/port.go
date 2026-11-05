package email

import (
	"context"
	"errors"
	"time"
)

// ErrAuth: the mail server refused the address or the app password.
var ErrAuth = errors.New("Google refused the address or app password")

// ErrState: not possible in the current state (connect while connected, disconnect while not).
var ErrState = errors.New("not possible in this state")

// Fetched is one message as the server holds it. Date is its INTERNALDATE.
type Fetched struct {
	UID   uint32
	Date  time.Time
	Draft bool
	Raw   []byte
}

// Session is an open, logged-in, read-only view of the mailbox holding every email (Gmail's All
// Mail). It is read-only by construction: nothing in it can change the mailbox.
type Session interface {
	UIDValidity() uint32
	UIDNext() uint32
	// UIDsSince lists the UIDs of messages on or after day (a zero day: every message).
	UIDsSince(ctx context.Context, day time.Time) ([]uint32, error)
	UIDsAfter(ctx context.Context, uid uint32) ([]uint32, error)
	// Fetch reads the messages without marking them read (BODY.PEEK).
	Fetch(ctx context.Context, uids []uint32) ([]Fetched, error)
	// Wait returns when the mailbox changes, after max, or when the session ends.
	Wait(ctx context.Context, max time.Duration) error
	Close() error
}

// Dial logs in and opens the all-mail mailbox read-only; a refused login is ErrAuth.
type Dial func(ctx context.Context, address, password string) (Session, error)

// ErrRenumbered: the mailbox's UIDVALIDITY isn't the one given; nothing was changed.
var ErrRenumbered = errors.New("mailbox renumbered")

// Reconcile is the one door that may change the mailbox. On one short connection it sets \Seen on
// mark, then returns the UIDs unread in the Inbox's Primary tab. ErrAuth when the login is refused.
// It is apart from Session, which stays read-only.
type Reconcile func(ctx context.Context, address, password string, uidValidity uint32, mark []uint32) (unread []uint32, err error)

// Outgoing is an email the owner sends: plain text to one recipient; InReplyTo (a Message-ID
// without brackets) makes it a reply in that thread. MessageID (without brackets) is its own,
// chosen by Service.Send, so a setup check knows the thread its reply lands in.
type Outgoing struct {
	To, Subject, Text, InReplyTo, MessageID string
}

// Send sends m from address over SMTP, logged in with the app password; a refused login is
// ErrAuth. Sending is apart from Session: the Session stays read-only, and Gmail files what was sent
// in All Mail, where the archive reads it like any other email.
type Send func(ctx context.Context, address, password string, m Outgoing) error
