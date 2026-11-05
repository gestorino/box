// Package email is emaild: it archives every email of one mailbox — received and sent, with
// attachments — over IMAP (the archive session never writes; a separate connection sets \Seen on
// what the owner opens), sends the owner's emails over SMTP, and serves the archive to
// the engine.
// Design: docs/superpowers/specs/2026-10-28-email-design.md.
package email

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // decode the charsets mail actually uses
	"github.com/emersion/go-message/mail"
	"golang.org/x/net/html"
)

// Attachment is one part of an email that is not its body, numbered in the order it appears.
type Attachment struct {
	Idx         int    `json:"idx"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// Parsed is what the archive keeps beside the original message. Thread is the conversation's root
// Message-ID ("" when the email names none); Out is true when the account sent it; Text is the
// text/plain body ("" when there is none); Snippet comes from it, or from the HTML body's words.
type Parsed struct {
	MessageID, Thread, FromName, FromAddr, Subject, Snippet, Text string
	To, Cc                                                        []string
	HasHTML, Out                                                  bool
	Attachments                                                   []Attachment
}

var errNoHTML = errors.New("no HTML part")

const snippetRunes = 200

type part struct {
	att  Attachment
	body []byte
}

type parts struct {
	header      mail.Header
	text, html  []byte
	hasText     bool
	hasHTML     bool
	attachments []part
}

// split walks the message once: the first text/plain and text/html leaves that are not marked as
// attachments are its body; every other leaf is an attachment.
func split(raw []byte) (parts, error) {
	ent, err := message.Read(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return parts{}, err
	}
	p := parts{header: mail.Header{Header: ent.Header}}
	err = ent.Walk(func(_ []int, e *message.Entity, err error) error {
		if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
			return err
		}
		if e.MultipartReader() != nil {
			return nil
		}
		body, err := io.ReadAll(e.Body)
		if err != nil {
			return err
		}
		ct, _, _ := e.Header.ContentType()
		disp, _, _ := e.Header.ContentDisposition()
		switch {
		case ct == "text/plain" && disp != "attachment" && !p.hasText:
			p.text, p.hasText = body, true
		case ct == "text/html" && disp != "attachment" && !p.hasHTML:
			p.html, p.hasHTML = body, true
		default:
			name, _ := (&mail.AttachmentHeader{Header: e.Header}).Filename()
			if ct == "" {
				ct = "application/octet-stream"
			}
			p.attachments = append(p.attachments, part{
				att:  Attachment{Idx: len(p.attachments), Filename: baseName(name), ContentType: ct, Size: int64(len(body))},
				body: body,
			})
		}
		return nil
	})
	return p, err
}

// baseName keeps only a file name: no directories, no way to climb out of one.
func baseName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = strings.TrimSpace(name[strings.LastIndex(name, "/")+1:])
	if name == "." || name == ".." {
		return ""
	}
	return name
}

func Parse(raw []byte, account string) (Parsed, error) {
	p, err := split(raw)
	if err != nil {
		return Parsed{}, err
	}
	h := p.header
	out := Parsed{HasHTML: p.hasHTML}
	out.MessageID, _ = h.MessageID()
	out.Thread = out.MessageID
	if ids, _ := h.MsgIDList("In-Reply-To"); len(ids) > 0 {
		out.Thread = ids[0]
	}
	if ids, _ := h.MsgIDList("References"); len(ids) > 0 {
		out.Thread = ids[0]
	}
	out.Subject, _ = h.Subject()
	if from, _ := h.AddressList("From"); len(from) > 0 {
		out.FromName, out.FromAddr = from[0].Name, from[0].Address
		out.Out = strings.EqualFold(from[0].Address, account)
	}
	out.To, out.Cc = addresses(h, "To"), addresses(h, "Cc")
	out.Text = string(p.text) // "" for an HTML-only email: the console shows its HTML instead
	if p.hasText {
		out.Snippet = snippet(out.Text)
	} else if p.hasHTML {
		out.Snippet = snippet(htmlText(p.html))
	}
	for _, a := range p.attachments {
		out.Attachments = append(out.Attachments, a.att)
	}
	return out, nil
}

func addresses(h mail.Header, key string) []string {
	list, _ := h.AddressList(key)
	out := make([]string, 0, len(list))
	for _, a := range list {
		if a.Name != "" {
			out = append(out, a.Name+" <"+a.Address+">")
		} else {
			out = append(out, a.Address)
		}
	}
	return out
}

func snippet(text string) string {
	r := []rune(strings.Join(strings.Fields(text), " "))
	if len(r) > snippetRunes {
		r = r[:snippetRunes]
	}
	return string(r)
}

// htmlText is the words of an HTML body, without its scripts and styles.
func htmlText(body []byte) string {
	z := html.NewTokenizer(bytes.NewReader(body))
	var b strings.Builder
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			return strings.Join(strings.Fields(b.String()), " ")
		case html.StartTagToken:
			if name, _ := z.TagName(); string(name) == "script" || string(name) == "style" {
				skip++
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); (string(name) == "script" || string(name) == "style") && skip > 0 {
				skip--
			}
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
				b.WriteByte(' ')
			}
		}
	}
}

// OpenAttachment is attachment idx and its decoded bytes.
func OpenAttachment(raw []byte, idx int) (Attachment, []byte, error) {
	p, err := split(raw)
	if err != nil {
		return Attachment{}, nil, err
	}
	if idx < 0 || idx >= len(p.attachments) {
		return Attachment{}, nil, fmt.Errorf("no attachment %d", idx)
	}
	a := p.attachments[idx]
	return a.att, a.body, nil
}

// loadsOnItsOwn are the elements that can fetch or navigate without a click in ways a CSP does not
// govern — a meta refresh navigates the frame itself, resource hints reach the network in browsers
// regardless — plus the active ones the sandbox already stops, removed as well.
var loadsOnItsOwn = map[string]bool{
	"meta": true, "link": true, "base": true, "script": true, "object": true, "embed": true, "iframe": true, "form": true,
}

// HTMLDocument is the email's HTML body without anything that loads or navigates on its own, every
// link set to open a new tab. The console also serves it under a CSP and an iframe sandbox that
// stop scripts and every remote load but images.
func HTMLDocument(raw []byte) ([]byte, error) {
	p, err := split(raw)
	if err != nil {
		return nil, err
	}
	if !p.hasHTML {
		return nil, errNoHTML
	}
	doc, err := html.Parse(bytes.NewReader(p.html))
	if err != nil {
		return nil, err
	}
	var drop []*html.Node
	for n := range doc.Descendants() {
		switch {
		case n.Type != html.ElementNode:
		case loadsOnItsOwn[n.Data]:
			drop = append(drop, n)
		case n.Data == "a" && hasAttr(n, "href"):
			setAttr(n, "target", "_blank")
			setAttr(n, "rel", "noopener noreferrer")
		}
	}
	for _, n := range drop {
		if n.Parent != nil {
			n.Parent.RemoveChild(n)
		}
	}
	var b bytes.Buffer
	if err := html.Render(&b, doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}
