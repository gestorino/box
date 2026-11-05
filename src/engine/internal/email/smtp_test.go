package email

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// smtpServer is an in-process SMTP server on loopback that keeps what it was sent.
type smtpServer struct {
	mu               sync.Mutex
	user, pass       string // the login it accepts
	gotUser, gotPass string
	from             string
	to               []string
	data             string
}

func (s *smtpServer) NewSession(*smtp.Conn) (smtp.Session, error) { return &smtpSession{s: s}, nil }

type smtpSession struct{ s *smtpServer }

func (x *smtpSession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (x *smtpSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, user, pass string) error {
		x.s.mu.Lock()
		defer x.s.mu.Unlock()
		x.s.gotUser, x.s.gotPass = user, pass
		if user != x.s.user || pass != x.s.pass {
			return &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "Username and Password not accepted"}
		}
		return nil
	}), nil
}
func (x *smtpSession) Mail(from string, _ *smtp.MailOptions) error {
	x.s.mu.Lock()
	x.s.from = from
	x.s.mu.Unlock()
	return nil
}
func (x *smtpSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	x.s.mu.Lock()
	x.s.to = append(x.s.to, to)
	x.s.mu.Unlock()
	return nil
}
func (x *smtpSession) Data(r io.Reader) error {
	b, err := io.ReadAll(r)
	x.s.mu.Lock()
	x.s.data = string(b)
	x.s.mu.Unlock()
	return err
}
func (x *smtpSession) Reset()        {}
func (x *smtpSession) Logout() error { return nil }

func startSMTP(t *testing.T) (*smtpServer, string) {
	t.Helper()
	b := &smtpServer{user: me, pass: "abcdefghijklmnop"}
	s := smtp.NewServer(b)
	s.Domain, s.AllowInsecureAuth = "localhost", true
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve(ln)
	t.Cleanup(func() { s.Close() })
	return b, ln.Addr().String()
}

func TestSMTPSendsPlainText(t *testing.T) {
	b, addr := startSMTP(t)
	send := NewSMTPSend(addr, false)
	err := send(ctx, me, "abcdefghijklmnop", Outgoing{To: "ana@example.com", Subject: "Re: Lease", Text: "Fine — thursday.", InReplyTo: "root@x", MessageID: "abc@concierge"})
	if err != nil {
		t.Fatal(err)
	}
	if b.gotUser != me || b.gotPass != "abcdefghijklmnop" || b.from != me || len(b.to) != 1 || b.to[0] != "ana@example.com" {
		t.Fatalf("%+v", b)
	}
	for _, want := range []string{"From: <" + me + ">", "To: <ana@example.com>", "Subject: Re: Lease", "Date: ", "Message-Id: <abc@concierge>",
		"Content-Type: text/plain; charset=utf-8", "In-Reply-To: <root@x>", "References: <root@x>"} {
		if !strings.Contains(b.data, want) {
			t.Errorf("no %q in\n%s", want, b.data)
		}
	}
	if !strings.Contains(b.data, "thursday") {
		t.Errorf("no body in\n%s", b.data)
	}

	b.data = ""
	if err := send(ctx, me, "abcdefghijklmnop", Outgoing{To: "ana@example.com", Subject: "Hi", Text: "x", MessageID: "def@concierge"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.data, "In-Reply-To") || strings.Contains(b.data, "References") {
		t.Fatalf("a new email is not a reply:\n%s", b.data)
	}
}

func TestSMTPRefusedAuthIsErrAuth(t *testing.T) {
	_, addr := startSMTP(t)
	err := NewSMTPSend(addr, false)(ctx, me, "wrong", Outgoing{To: "ana@example.com", Subject: "Hi", Text: "x"})
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("%v", err)
	}
}
