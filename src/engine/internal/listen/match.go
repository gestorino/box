// Package listen routes each newly stored incoming message to the listeners whose filter it
// matches. A connector (emaild, smsd, whatsappd) owns one Hub; the engine's open Listen calls are its subs.
package listen

import (
	"net/mail"
	"strings"

	listenv1 "concierge/engine/internal/gen/listen/v1"
)

// Match says whether a message passes a listener's filter. Connectors route only: they never
// read a message's meaning.
type Match func(req *listenv1.ListenRequest, m *listenv1.Message) bool

// MatchEmail: From is the sender's address exactly, To is one of the recipients' addresses,
// SubjectContains is a case-insensitive part of the subject; an empty field matches anything.
// A message that is not an email never matches.
func MatchEmail(req *listenv1.ListenRequest, m *listenv1.Message) bool {
	e := m.GetEmail()
	if e == nil {
		return false
	}
	if req.GetFrom() != "" && !strings.EqualFold(req.GetFrom(), e.GetFromAddr()) {
		return false
	}
	if req.GetTo() != "" && !hasRecipient(e.GetTo(), req.GetTo()) {
		return false
	}
	return contains(e.GetSubject(), req.GetSubjectContains())
}

// MatchSMS: From is a case-insensitive part of the sender's number or name. A message that is not
// an SMS never matches.
func MatchSMS(req *listenv1.ListenRequest, m *listenv1.Message) bool {
	s := m.GetSms()
	if s == nil {
		return false
	}
	return contains(s.GetNumber(), req.GetFrom())
}

// MatchWhatsApp: From is the chat's phone, compared as digits only on both sides (so "+351 91..."
// matches "35191..."), exactly, never a part. A message that is not a WhatsApp message never matches.
func MatchWhatsApp(req *listenv1.ListenRequest, m *listenv1.Message) bool {
	w := m.GetWhatsapp()
	if w == nil {
		return false
	}
	want := digits(req.GetFrom())
	return want == "" || want == digits(w.GetPhone())
}

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// hasRecipient reports whether want is the address of one of the recipients, each of which may
// be "Name <addr>"; one that does not parse is compared as it is.
func hasRecipient(recipients []string, want string) bool {
	for _, r := range recipients {
		addr := r
		if a, err := mail.ParseAddress(r); err == nil {
			addr = a.Address
		}
		if strings.EqualFold(addr, want) {
			return true
		}
	}
	return false
}

func contains(s, part string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(part))
}
