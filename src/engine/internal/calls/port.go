// Package calls is calld: it follows every call on the modem — incoming and outgoing — records when
// it started, was answered and ended and how it ended, clears finished calls from ModemManager,
// serves the history to the engine, and places the owner's calls and the setup check, held by
// OpenAI's voice model on the owner's OpenAI key (openai.go).
// Design: docs/superpowers/specs/2026-10-27-phone-archive-design.md (Revision 2026-10-28); the key:
// docs/superpowers/specs/2026-11-03-voice-openai-key-design.md.
package calls

import (
	"context"
	"errors"
)

// ModemCall is a call as ModemManager holds it. Direction incoming|outgoing; State dialing,
// ringing-out, ringing-in, active, held, waiting, terminated; Reason accepted, terminated,
// refused-or-busy, error, audio-setup-failed, ….
type ModemCall struct{ Path, Number, Direction, State, Reason string }

var ErrModemChanged = errors.New("ModemManager restarted since the list was read")

// Modem lists and clears calls, addressed to the ModemManager instance the list came from, and
// places one: Dial creates and starts it, Hangup ends it.
type Modem interface {
	List(ctx context.Context) (instance string, calls []ModemCall, err error)
	Delete(ctx context.Context, instance, path string) error
	Dial(ctx context.Context, number string) (instance, path string, err error)
	Hangup(ctx context.Context, instance, path string) error
}
