package main

import "testing"

func TestTheDefaultsAreTheService(t *testing.T) {
	if defaultControlPlane != "https://gestorino.com" || defaultBoxDomain != "gestorino.net" {
		t.Fatalf("defaults %q %q", defaultControlPlane, defaultBoxDomain)
	}
	if err := checkDomains(defaultControlPlane, defaultBoxDomain); err != nil {
		t.Fatalf("the defaults refused: %v", err)
	}
}

func TestCheckDomains(t *testing.T) {
	for _, tc := range []struct{ cp, bd string }{
		{"https://gestorino.com/", "gestorino.net"},
		{"https://cp.example.org", "box.example.net"},
	} {
		if err := checkDomains(tc.cp, tc.bd); err != nil {
			t.Errorf("%q %q refused: %v", tc.cp, tc.bd, err)
		}
	}
	for _, cp := range []string{
		"http://gestorino.com", "http://127.0.0.1:8080", "gestorino.com", "https://", "ftp://gestorino.com",
		"https://u:p@gestorino.com", "https://gestorino.com/box", "https://gestorino.com?x=1", "https://gestorino.com#x", "",
	} {
		if err := checkDomains(cp, "gestorino.net"); err == nil {
			t.Errorf("control plane %q accepted", cp)
		}
	}
	for _, bd := range []string{
		"", "gestorino.net.", "Gestorino.net", "GESTORINO.NET", "gestorino", "gestorino..net", ".gestorino.net",
		"-gestorino.net", "gestorino-.net", "gestorino.net:7000", "gestorino_x.net", "https://gestorino.net", "gestorino.net/x",
	} {
		if err := checkDomains("https://gestorino.com", bd); err == nil {
			t.Errorf("box domain %q accepted", bd)
		}
	}
}
