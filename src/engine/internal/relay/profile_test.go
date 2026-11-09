package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"strings"
	"testing"
)

// profileValues reads the top-level keys of a profile and their values, enough to check it without a plist library.
func profileValues(t *testing.T, doc []byte) map[string]string {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(doc))
	vals := map[string]string{}
	var key string
	depth := 0
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch el := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 3 { // plist > dict > (key|value)
				var s string
				if err := d.DecodeElement(&s, &el); err != nil && el.Name.Local != "array" {
					t.Fatalf("decode %s: %v", el.Name.Local, err)
				}
				depth--
				if el.Name.Local == "key" {
					key = s
				} else {
					vals[key] = s
				}
			}
		case xml.EndElement:
			depth--
		}
	}
	return vals
}

func TestProfileNamesTheCertificateAndCarriesTheFile(t *testing.T) {
	p12 := []byte("not really a p12 \x00\xff")
	doc, err := Profile("Nik's <iPhone> & co", "3c9bfc8c", p12)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(doc, []byte(`<?xml version="1.0" encoding="UTF-8"?>`)) {
		t.Fatalf("not an XML plist: %.60s", doc)
	}
	if err := xml.Unmarshal(doc, new(struct{})); err != nil {
		t.Fatalf("not well-formed (the label must be escaped): %v", err)
	}
	top := profileValues(t, doc)
	if top["PayloadDisplayName"] != "Gestorino Cert" {
		t.Fatalf("display name %q", top["PayloadDisplayName"])
	}
	if top["PayloadType"] != "Configuration" || top["PayloadVersion"] != "1" {
		t.Fatalf("type %q version %q", top["PayloadType"], top["PayloadVersion"])
	}
	if !strings.HasSuffix(top["PayloadIdentifier"], ".3c9bfc8c") || len(top["PayloadUUID"]) != 36 {
		t.Fatalf("identifier %q uuid %q", top["PayloadIdentifier"], top["PayloadUUID"])
	}
	s := string(doc)
	for _, want := range []string{
		"<string>com.apple.security.pkcs12</string>",
		"<data>" + base64.StdEncoding.EncodeToString(p12) + "</data>",
		"Nik&#39;s &lt;iPhone&gt; &amp; co",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("profile lacks %q:\n%s", want, s)
		}
	}
	// No password inside: the phone asks for it while installing, so the file alone opens nothing.
	if strings.Contains(s, "<key>Password</key>") {
		t.Fatal("the profile carries the password")
	}
	again, _ := Profile("Nik's <iPhone> & co", "3c9bfc8c", p12)
	if profileValues(t, again)["PayloadUUID"] == top["PayloadUUID"] {
		t.Fatal("two profiles share a UUID")
	}
}
