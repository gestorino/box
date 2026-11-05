package email

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
)

const gmailAllMail = "[Gmail]/Todo o correio"

// gmailWire is a scripted IMAP server on loopback, in the clear: it answers the few commands the
// reconcile sends the way Gmail does, and records every line it receives (the LOGIN's credentials
// are never recorded).
type gmailWire struct {
	mu       sync.Mutex
	lines    []string
	validity uint32
	found    string // the UIDs the SEARCH answers with
	refuse   bool   // LOGIN is answered NO
}

func newGmailWire(t *testing.T) (*gmailWire, string) {
	t.Helper()
	w := &gmailWire{validity: 7, found: "3 5"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go w.serve(conn)
		}
	}()
	return w, ln.Addr().String()
}

func (w *gmailWire) record(line string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, line)
}

// received is what the server was sent, but for CAPABILITY, which the client asks on its own.
func (w *gmailWire) received() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(w.lines), func(l string) bool { return l == "CAPABILITY" })
}

func (w *gmailWire) serve(conn net.Conn) {
	defer conn.Close()
	fmt.Fprint(conn, "* OK ready\r\n")
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		tag, cmd, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		verb, _, _ := strings.Cut(cmd, " ")
		if verb == "LOGIN" {
			cmd = "LOGIN"
		}
		w.record(cmd)
		switch verb {
		case "CAPABILITY":
			fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1\r\n%s OK done\r\n", tag)
		case "LOGIN":
			if w.refuse {
				fmt.Fprintf(conn, "%s NO [AUTHENTICATIONFAILED] Invalid credentials\r\n", tag)
				continue
			}
			fmt.Fprintf(conn, "%s OK logged in\r\n", tag)
		case "LIST":
			fmt.Fprintf(conn, "* LIST (\\HasNoChildren) \"/\" \"INBOX\"\r\n* LIST (\\HasNoChildren \\All) \"/\" %q\r\n%s OK done\r\n", gmailAllMail, tag)
		case "SELECT":
			fmt.Fprintf(conn, "* 9 EXISTS\r\n* OK [UIDVALIDITY %d] ok\r\n%s OK [READ-WRITE] done\r\n", w.validity, tag)
		case "UID":
			if strings.HasPrefix(cmd, "UID SEARCH") {
				fmt.Fprintf(conn, "* SEARCH %s\r\n", w.found)
			}
			fmt.Fprintf(conn, "%s OK done\r\n", tag)
		case "LOGOUT":
			fmt.Fprintf(conn, "* BYE\r\n%s OK bye\r\n", tag)
			return
		default:
			fmt.Fprintf(conn, "%s BAD unexpected\r\n", tag)
		}
	}
}

func TestReconcileMarksThenSearches(t *testing.T) {
	w, addr := newGmailWire(t)
	unread, err := NewGmailReconcile(addr, false)(ctx, me, "app-password", 7, []uint32{9, 4})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(unread, []uint32{3, 5}) {
		t.Fatalf("%v", unread)
	}
	want := []string{
		"LOGIN",
		"LIST \"\" \"*\"",
		"SELECT \"" + gmailAllMail + "\"",
		`UID STORE 4,9 +FLAGS.SILENT (\Seen)`,
		`UID SEARCH X-GM-RAW "in:inbox category:primary is:unread"`,
		"LOGOUT",
	}
	if got := w.received(); !slices.Equal(got, want) {
		t.Fatalf("wire:\n%q\nwant:\n%q", got, want)
	}
}

func TestReconcileWithNothingToMarkSendsNoStore(t *testing.T) {
	w, addr := newGmailWire(t)
	if _, err := NewGmailReconcile(addr, false)(ctx, me, "app-password", 7, nil); err != nil {
		t.Fatal(err)
	}
	for _, l := range w.received() {
		if strings.Contains(l, "STORE") {
			t.Fatalf("sent %q", l)
		}
	}
}

func TestReconcileNeverStoresUID0(t *testing.T) {
	w, addr := newGmailWire(t)
	if _, err := NewGmailReconcile(addr, false)(ctx, me, "app-password", 7, []uint32{4, 0}); err == nil {
		t.Fatal("no error")
	}
	for _, l := range w.received() {
		if strings.Contains(l, "STORE") {
			t.Fatalf("sent %q", l) // go-imap would have sent "*", the newest message
		}
	}
}

func TestReconcileRefusesARenumberedMailbox(t *testing.T) {
	w, addr := newGmailWire(t)
	w.validity = 8
	_, err := NewGmailReconcile(addr, false)(ctx, me, "app-password", 7, []uint32{4})
	if !errors.Is(err, ErrRenumbered) {
		t.Fatalf("%v", err)
	}
	for _, l := range w.received() {
		if strings.Contains(l, "STORE") || strings.Contains(l, "SEARCH") {
			t.Fatalf("sent %q", l)
		}
	}
}

func TestReconcileRefusedLogin(t *testing.T) {
	w, addr := newGmailWire(t)
	w.refuse = true
	_, err := NewGmailReconcile(addr, false)(ctx, me, "wrong", 7, []uint32{4})
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("%v", err)
	}
	if got := w.received(); len(got) != 1 {
		t.Fatalf("%q", got)
	}
}

func TestReconcileErrorsCarryNoSecrets(t *testing.T) {
	// the server hangs up after its greeting: whatever the error says, it must not quote the password
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			fmt.Fprint(conn, "* OK ready\r\n")
			bufio.NewReader(conn).ReadString('\n') // the first command
			conn.Close()
		}
	}()
	_, err = NewGmailReconcile(ln.Addr().String(), false)(ctx, me, "app-password-xyz", 7, nil)
	if err == nil || strings.Contains(err.Error(), "app-password-xyz") {
		t.Fatalf("%v", err)
	}
}
