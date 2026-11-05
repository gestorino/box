package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

const smtpTimeout = time.Minute

// NewSMTPSend sends through server (smtp.gmail.com:465, implicit TLS); a plain connection is only
// for the tests' loopback server.
func NewSMTPSend(server string, implicitTLS bool) Send {
	return func(ctx context.Context, address, password string, m Outgoing) error {
		msg, err := compose(address, m)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
		defer cancel()
		var conn net.Conn
		if implicitTLS {
			host, _, _ := net.SplitHostPort(server)
			conn, err = (&tls.Dialer{Config: &tls.Config{ServerName: host}}).DialContext(ctx, "tcp", server)
		} else {
			conn, err = (&net.Dialer{}).DialContext(ctx, "tcp", server)
		}
		if err != nil {
			return err
		}
		deadline, _ := ctx.Deadline()
		conn.SetDeadline(deadline)
		c := smtp.NewClient(conn)
		defer c.Close()
		if err := c.Auth(sasl.NewPlainClient("", address, password)); err != nil {
			var se *smtp.SMTPError
			if errors.As(err, &se) && se.Code == 535 {
				return ErrAuth
			}
			return err
		}
		if err := c.SendMail(address, []string{m.To}, bytes.NewReader(msg)); err != nil {
			return err
		}
		return c.Quit()
	}
}

// compose writes m as a plain-text email from address.
func compose(address string, m Outgoing) ([]byte, error) {
	var h mail.Header
	h.SetAddressList("From", []*mail.Address{{Address: address}})
	h.SetAddressList("To", []*mail.Address{{Address: m.To}})
	h.SetSubject(m.Subject)
	h.SetDate(time.Now())
	h.SetMessageID(m.MessageID)
	if m.InReplyTo != "" {
		h.SetMsgIDList("In-Reply-To", []string{m.InReplyTo})
		h.SetMsgIDList("References", []string{m.InReplyTo})
	}
	h.SetContentType("text/plain", map[string]string{"charset": "utf-8"})
	var buf bytes.Buffer
	w, err := mail.CreateSingleInlineWriter(&buf, h)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(m.Text)); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
