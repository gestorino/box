package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/responses"
)

// primaryUnread is Gmail's search for the Inbox's Primary tab, unread: its X-GM-RAW extension is the
// only way to reach the tabs, and go-imap v2 cannot send it.
const primaryUnread = "in:inbox category:primary is:unread"

// NewGmailReconcile reconciles over IMAP on server (host:port) — over TLS, or in the clear for tests
// on loopback. One short connection: LOGIN, LIST, SELECT of the \All mailbox, UID STORE +FLAGS.SILENT
// (\Seen) when there is anything to mark, UID SEARCH X-GM-RAW, LOGOUT. Nothing else is ever sent.
// go-imap v1, because v2 has no way to send the search.
func NewGmailReconcile(server string, useTLS bool) Reconcile {
	return func(ctx context.Context, address, password string, uidValidity uint32, mark []uint32) ([]uint32, error) {
		raw, err := (&net.Dialer{Timeout: 30 * time.Second}).DialContext(ctx, "tcp", server)
		if err != nil {
			return nil, err
		}
		defer context.AfterFunc(ctx, func() { raw.Close() })() // ends whatever command is waiting
		conn := raw
		if useTLS {
			host, _, _ := net.SplitHostPort(server)
			tc := tls.Client(raw, &tls.Config{ServerName: host})
			if err := tc.HandshakeContext(ctx); err != nil {
				raw.Close()
				return nil, err
			}
			conn = tc
		}
		c, err := client.New(&quietConn{Conn: conn, quiet: quiet})
		if err != nil {
			raw.Close()
			return nil, fmt.Errorf("greeting: %w", err)
		}
		defer c.Terminate()
		c.ErrorLog = log.New(io.Discard, "", 0) // the client's own log of a read error can quote the server
		if err := c.Login(address, password); err != nil {
			if refusal(c, err) {
				return nil, ErrAuth
			}
			return nil, failed(c, "login", err)
		}
		defer c.Logout()
		name, err := allMail(c)
		if err != nil {
			return nil, err
		}
		box, err := c.Select(name, false)
		if err != nil {
			return nil, failed(c, "select", err)
		}
		if box.UidValidity != uidValidity {
			return nil, ErrRenumbered
		}
		if len(mark) > 0 {
			set := new(imap.SeqSet)
			for _, u := range mark {
				if u == 0 { // go-imap reads 0 as "*": the newest message
					return nil, errors.New("refusing to mark UID 0")
				}
				set.AddNum(u)
			}
			seen := []interface{}{imap.SeenFlag}
			if err := c.UidStore(set, imap.FormatFlagsOp(imap.AddFlags, true), seen, nil); err != nil {
				return nil, failed(c, "store", err)
			}
		}
		found := &responses.Search{}
		st, err := c.Execute(&imap.Command{Name: "UID", Arguments: []interface{}{
			imap.RawString("SEARCH"), imap.RawString("X-GM-RAW"), primaryUnread}}, found)
		if err == nil {
			err = st.Err()
		}
		if err != nil {
			return nil, failed(c, "search", err)
		}
		return found.Ids, nil
	}
}

// allMail finds the mailbox flagged \All (Gmail's All Mail, whatever language names it).
func allMail(c *client.Client) (string, error) {
	boxes := make(chan *imap.MailboxInfo, 100)
	done := make(chan error, 1)
	go func() { done <- c.List("", "*", boxes) }()
	name := ""
	for b := range boxes {
		for _, attr := range b.Attributes {
			if attr == imap.AllAttr {
				name = b.Name
			}
		}
	}
	if err := <-done; err != nil {
		return "", failed(c, "list", err)
	}
	if name == "" {
		return "", errors.New(`no mailbox flagged \All (Gmail's All Mail)`)
	}
	return name, nil
}

// refusal reports whether err is the server answering NO or BAD (go-imap v1 gives such an answer as a
// plain error holding the server's text), not the connection failing.
func refusal(c *client.Client, err error) bool {
	select {
	case <-c.LoggedOut():
		return false
	default:
	}
	var ne net.Error
	return !errors.As(err, &ne) && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe)
}

// failed names the step that failed. A server's refusal is reported without its text, which is the
// server's own: nothing of the login or of any mail reaches an error.
func failed(c *client.Client, step string, err error) error {
	if refusal(c, err) {
		return fmt.Errorf("%s: refused by the server", step)
	}
	return fmt.Errorf("%s: %w", step, err)
}
