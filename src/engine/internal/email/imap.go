package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// quiet is how long a read may wait for a single byte before the session is given up. It bounds a
// server that stops answering without bounding a slow, moving download (ten large emails over
// mobile data); while IDLE waits for new mail, silence is normal and the limit is suspended.
const quiet = 2 * time.Minute

// quietConn ends a read that receives nothing for quiet, unless suspended.
type quietConn struct {
	net.Conn
	quiet     time.Duration
	suspended atomic.Bool
}

func (c *quietConn) Read(p []byte) (int, error) {
	if c.suspended.Load() {
		c.Conn.SetReadDeadline(time.Time{})
	} else {
		c.Conn.SetReadDeadline(time.Now().Add(c.quiet))
	}
	return c.Conn.Read(p)
}

// suspend lifts (or restores) the limit, including for a read already waiting.
func (c *quietConn) suspend(on bool) {
	c.suspended.Store(on)
	if on {
		c.Conn.SetReadDeadline(time.Time{})
	} else {
		c.Conn.SetReadDeadline(time.Now().Add(c.quiet))
	}
}

// peek reads a whole message without setting \Seen.
var peek = &imap.FetchItemBodySection{Peek: true}

// NewIMAPDial opens sessions on server (host:port) — over TLS, or in the clear for tests on loopback.
// Thin over go-imap; read-only by what it sends: EXAMINE, UID SEARCH, UID FETCH BODY.PEEK[], IDLE.
func NewIMAPDial(server string, useTLS bool) Dial {
	return func(ctx context.Context, address, password string) (Session, error) {
		s := &imapSession{changed: make(chan struct{}, 1)}
		opts := &imapclient.Options{
			UnilateralDataHandler: &imapclient.UnilateralDataHandler{
				Mailbox: func(d *imapclient.UnilateralDataMailbox) {
					if d.NumMessages != nil {
						s.notify()
					}
				},
			},
		}
		conn, err := (&net.Dialer{Timeout: 30 * time.Second}).DialContext(ctx, "tcp", server)
		if err != nil {
			return nil, err
		}
		if useTLS {
			host, _, _ := net.SplitHostPort(server)
			tc := tls.Client(conn, &tls.Config{ServerName: host})
			if err := tc.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			conn = tc
		}
		s.conn = &quietConn{Conn: conn, quiet: quiet}
		s.c = imapclient.New(s.conn, opts)
		if err := s.open(ctx, address, password); err != nil {
			s.c.Close()
			return nil, err
		}
		return s, nil
	}
}

type imapSession struct {
	conn                 *quietConn
	c                    *imapclient.Client
	uidValidity, uidNext uint32
	changed              chan struct{}
}

func (s *imapSession) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// guard closes the connection when ctx ends before the command does; call the returned func when
// the command is done. (A server gone silent is quietConn's to end.)
func (s *imapSession) guard(ctx context.Context) func() {
	stop := context.AfterFunc(ctx, func() { s.c.Close() })
	return func() { stop() }
}

func (s *imapSession) open(ctx context.Context, address, password string) error {
	defer s.guard(ctx)()
	if err := s.c.Login(address, password).Wait(); err != nil {
		var ie *imap.Error
		if errors.As(err, &ie) && (ie.Code == imap.ResponseCodeAuthenticationFailed || ie.Type == imap.StatusResponseTypeNo) {
			return ErrAuth
		}
		return fmt.Errorf("login: %w", err)
	}
	var opts *imap.ListOptions
	if s.c.Caps().Has(imap.CapSpecialUse) {
		opts = &imap.ListOptions{ReturnSpecialUse: true}
	}
	boxes, err := s.c.List("", "*", opts).Collect()
	if err != nil {
		return err
	}
	name := ""
	for _, b := range boxes {
		if slices.Contains(b.Attrs, imap.MailboxAttrAll) {
			name = b.Mailbox
		}
	}
	if name == "" {
		return errors.New(`no mailbox flagged \All (Gmail's All Mail)`)
	}
	data, err := s.c.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait() // EXAMINE
	if err != nil {
		return err
	}
	s.uidValidity, s.uidNext = data.UIDValidity, uint32(data.UIDNext)
	return nil
}

func (s *imapSession) UIDValidity() uint32 { return s.uidValidity }
func (s *imapSession) UIDNext() uint32     { return s.uidNext }

func (s *imapSession) search(ctx context.Context, c *imap.SearchCriteria) ([]uint32, error) {
	defer s.guard(ctx)()
	data, err := s.c.UIDSearch(c, nil).Wait()
	if err != nil {
		return nil, err
	}
	var out []uint32
	for _, u := range data.AllUIDs() {
		out = append(out, uint32(u))
	}
	return out, nil
}

func (s *imapSession) UIDsSince(ctx context.Context, day time.Time) ([]uint32, error) {
	return s.search(ctx, &imap.SearchCriteria{Since: day})
}

func (s *imapSession) UIDsAfter(ctx context.Context, uid uint32) ([]uint32, error) {
	all, err := s.search(ctx, &imap.SearchCriteria{UID: []imap.UIDSet{{imap.UIDRange{Start: imap.UID(uid + 1)}}}})
	// "n:*" always names the last message, even when its UID is below n.
	return slices.DeleteFunc(all, func(u uint32) bool { return u <= uid }), err
}

func (s *imapSession) Fetch(ctx context.Context, uids []uint32) ([]Fetched, error) {
	set := imap.UIDSet{}
	for _, u := range uids {
		set.AddNum(imap.UID(u))
	}
	defer s.guard(ctx)()
	msgs, err := s.c.Fetch(set, &imap.FetchOptions{
		UID: true, Flags: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{peek},
	}).Collect()
	if err != nil {
		return nil, err
	}
	out := make([]Fetched, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Fetched{UID: uint32(m.UID), Date: m.InternalDate, Draft: slices.Contains(m.Flags, imap.FlagDraft),
			Raw: m.FindBodySection(peek)})
	}
	slices.SortFunc(out, func(a, b Fetched) int { return int(a.UID) - int(b.UID) })
	return out, nil
}

func (s *imapSession) Wait(ctx context.Context, max time.Duration) error {
	s.conn.suspend(true)
	defer s.conn.suspend(false)
	idle, err := s.c.Idle()
	if err != nil {
		return err
	}
	t := time.NewTimer(max)
	defer t.Stop()
	select {
	case <-s.changed:
	case <-t.C:
	case <-ctx.Done():
	case <-s.c.Closed():
	}
	if err := idle.Close(); err != nil {
		return err
	}
	return idle.Wait()
}

func (s *imapSession) Close() error { return s.c.Close() }
