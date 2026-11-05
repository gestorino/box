// Package sms is smsd: it stores every SMS the modem holds — received and sent — in its own
// database, then clears it from the modem, serves the archive to the engine, and sends what the owner
// sends from the console.
// Design: docs/superpowers/specs/2026-10-27-phone-archive-design.md (Revision 2026-10-28).
package sms

import (
	"context"
	"errors"
)

// ModemSMS is one message as ModemManager holds it. State is received, sent, receiving, stored
// (an unsent draft), sending, …; Timestamp is RFC 3339 UTC, empty for most sent messages.
type ModemSMS struct {
	Path, Number, Text, PDUType, State, Timestamp string
	Data                                          []byte
	Reference                                     uint32 // TP-MR of a sent message
}

// ErrModemChanged: ModemManager restarted since the list was read, so its paths now name other
// messages — nothing may be deleted by them.
var ErrModemChanged = errors.New("ModemManager restarted since the list was read")

// Modem lists the messages and deletes one, addressed to the ModemManager instance (its D-Bus unique
// name) the list came from, and sends one: what is sent stays in the list as "sent" and is archived
// from there like any other message.
type Modem interface {
	List(ctx context.Context) (instance string, msgs []ModemSMS, err error)
	Delete(ctx context.Context, instance, path string) error
	Send(ctx context.Context, number, text string) error
}
