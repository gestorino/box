package listen

import (
	"testing"

	listenv1 "concierge/engine/internal/gen/listen/v1"
)

func email(e *listenv1.Email) *listenv1.Message {
	return &listenv1.Message{Body: &listenv1.Message_Email{Email: e}}
}

func sms(number string) *listenv1.Message {
	return &listenv1.Message{Body: &listenv1.Message_Sms{Sms: &listenv1.SMS{Number: number}}}
}

func TestMatchEmailFromIsExactCaseInsensitive(t *testing.T) {
	req := &listenv1.ListenRequest{From: "Noreply@Site.example"}
	if !MatchEmail(req, email(&listenv1.Email{FromAddr: "noreply@site.example"})) {
		t.Error("same address, other case: want a match")
	}
	if MatchEmail(req, email(&listenv1.Email{FromAddr: "x-noreply@site.example"})) {
		t.Error("a longer address must not match")
	}
}

func TestMatchEmailEmptyFromIsAnySender(t *testing.T) {
	if !MatchEmail(&listenv1.ListenRequest{}, email(&listenv1.Email{FromAddr: "a@b.example"})) {
		t.Error("empty From: want any sender")
	}
}

func TestMatchEmailToIsOneOfTheRecipients(t *testing.T) {
	m := email(&listenv1.Email{To: []string{"Me <ME+a1@gmail.com>", "other@x"}})
	if !MatchEmail(&listenv1.ListenRequest{To: "me+a1@gmail.com"}, m) {
		t.Error("want a match on the parsed address")
	}
	if MatchEmail(&listenv1.ListenRequest{To: "me+a2@gmail.com"}, m) {
		t.Error("another plus-address must not match")
	}
	if !MatchEmail(&listenv1.ListenRequest{To: "other@x"}, m) {
		t.Error("a bare recipient should match")
	}
}

func TestMatchEmailSubjectContains(t *testing.T) {
	m := email(&listenv1.Email{Subject: "Please CONFIRM your account"})
	if !MatchEmail(&listenv1.ListenRequest{SubjectContains: "confirm"}, m) {
		t.Error("want a case-insensitive contains")
	}
	if MatchEmail(&listenv1.ListenRequest{SubjectContains: "receipt"}, m) {
		t.Error("absent text must not match")
	}
	if !MatchEmail(&listenv1.ListenRequest{}, m) {
		t.Error("empty matches anything")
	}
}

func TestMatchEmailRefusesAnSMS(t *testing.T) {
	if MatchEmail(&listenv1.ListenRequest{}, sms("+351912345678")) {
		t.Error("an SMS is not an email")
	}
}

func TestMatchSMSRefusesAnEmail(t *testing.T) {
	if MatchSMS(&listenv1.ListenRequest{}, email(&listenv1.Email{})) {
		t.Error("an email is not an SMS")
	}
}

func TestMatchSMSFromIsContainedCaseInsensitive(t *testing.T) {
	cases := []struct {
		from, number string
		want         bool
	}{
		{"booksy", "BOOKSY", true},
		{"912345678", "+351912345678", true},
		{"", "+351912345678", true},
		{"999", "+351912345678", false},
	}
	for _, c := range cases {
		if got := MatchSMS(&listenv1.ListenRequest{From: c.from}, sms(c.number)); got != c.want {
			t.Errorf("from %q number %q: got %v want %v", c.from, c.number, got, c.want)
		}
	}
}
