package email

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const me = "owner@example.com"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parse(t *testing.T, name string) Parsed {
	t.Helper()
	p, err := Parse(fixture(t, name), me)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParsePlain(t *testing.T) {
	p := parse(t, "plain.eml")
	if p.MessageID != "root@example.com" || p.Thread != "root@example.com" || p.FromName != "Ana Silva" ||
		p.FromAddr != "ana@example.com" || p.Subject != "Lease renewal" || p.Out || p.HasHTML {
		t.Fatalf("%+v", p)
	}
	if !slices.Equal(p.To, []string{"Alex <owner@example.com>", "bob@example.com"}) ||
		!slices.Equal(p.Cc, []string{"Carla <carla@example.com>"}) {
		t.Fatalf("to %q cc %q", p.To, p.Cc)
	}
	if !strings.Contains(p.Text, "ends next month.") || p.Snippet != "Hi, the lease ends next month." || len(p.Attachments) != 0 {
		t.Fatalf("text %q snippet %q attachments %v", p.Text, p.Snippet, p.Attachments)
	}
}

func TestParseHTMLOnlySnippet(t *testing.T) {
	p := parse(t, "html-only.eml")
	if !p.HasHTML || p.Snippet != "Your reservation in Porto is confirmed." || p.Text != "" {
		t.Fatalf("has_html %v text %q snippet %q", p.HasHTML, p.Text, p.Snippet)
	}
}

func TestParseAlternativePrefersText(t *testing.T) {
	p := parse(t, "alternative.eml")
	if !p.HasHTML || strings.TrimSpace(p.Text) != "Plain words" || len(p.Attachments) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestSnippetIs200Runes(t *testing.T) {
	raw := []byte("From: a@example.com\r\nSubject: long\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.Repeat("é ", 300))
	p, err := Parse(raw, me)
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(p.Snippet)); n != 200 {
		t.Fatalf("snippet is %d runes", n)
	}
}

func TestThreadFromReferences(t *testing.T) {
	if p := parse(t, "reply.eml"); p.Thread != "root@example.com" || p.MessageID != "reply-2@gmail.com" {
		t.Fatalf("%+v", p)
	}
	inReplyTo := []byte("From: a@example.com\r\nMessage-ID: <b@x>\r\nIn-Reply-To: <a@x>\r\nContent-Type: text/plain\r\n\r\nhi")
	if p, _ := Parse(inReplyTo, me); p.Thread != "a@x" {
		t.Fatalf("in-reply-to: %+v", p)
	}
	if p := parse(t, "no-message-id.eml"); p.Thread != "" || p.MessageID != "" {
		t.Fatalf("no ids: %+v", p)
	}
}

func TestDirectionIgnoresCase(t *testing.T) {
	if p := parse(t, "reply.eml"); !p.Out || p.FromAddr != "Owner@Example.com" {
		t.Fatalf("%+v", p)
	}
}

func TestAttachmentNames(t *testing.T) {
	raw := fixture(t, "attachments.eml")
	p, err := Parse(raw, me)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i, a := range p.Attachments {
		if a.Idx != i {
			t.Fatalf("idx %d at %d", a.Idx, i)
		}
		names = append(names, a.Filename)
	}
	if !slices.Equal(names, []string{"", "Contrato é.pdf", "résumé.txt", "passwd"}) || strings.TrimSpace(p.Text) != "See attached" {
		t.Fatalf("names %q text %q", names, p.Text)
	}
	if a := p.Attachments[1]; a.ContentType != "application/pdf" || a.Size != 9 {
		t.Fatalf("%+v", a)
	}
	for idx, want := range map[int][]byte{0: {1, 2, 3, 4}, 1: []byte("%PDF-1.4\n"), 3: []byte("root")} {
		a, body, err := OpenAttachment(raw, idx)
		if err != nil || !bytes.Equal(bytes.TrimRight(body, "\r\n"), bytes.TrimRight(want, "\r\n")) || a.Idx != idx {
			t.Fatalf("attachment %d: %+v %q %v", idx, a, body, err)
		}
	}
	if _, _, err := OpenAttachment(raw, 4); err == nil {
		t.Fatal("attachment 4 does not exist")
	}
}

func TestHTMLDocumentNeutralised(t *testing.T) {
	out, err := HTMLDocument(fixture(t, "hostile.eml"))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	links := 0
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "a" {
			continue
		}
		attrs := map[string]string{}
		for _, a := range n.Attr {
			attrs[a.Key] = a.Val
		}
		if _, ok := attrs["href"]; !ok {
			continue
		}
		links++
		if attrs["target"] != "_blank" || attrs["rel"] != "noopener noreferrer" {
			t.Fatalf("link %v", attrs)
		}
	}
	if links != 2 {
		t.Fatalf("%d links in %s", links, out)
	}
	// Nothing that can load or navigate on its own survives: CSP has no say over a frame's own
	// navigation (meta refresh) nor, in browsers, over resource hints.
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "meta", "link", "base", "script", "object", "embed", "iframe", "form":
				t.Fatalf("<%s> survived in %s", n.Data, out)
			}
		}
	}
	if !bytes.Contains(out, []byte("<style>")) || !bytes.Contains(out, []byte("Hello")) {
		t.Fatalf("the email's own styles and words must stay: %s", out)
	}
	if _, err := HTMLDocument(fixture(t, "plain.eml")); err == nil {
		t.Fatal("a text-only email has no HTML document")
	}
}
