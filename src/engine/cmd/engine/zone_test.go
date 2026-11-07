package main

import (
	"errors"
	"testing"
)

func TestZoneNameIsTheBoxsIANAName(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	link := func(v string, err error) func(string) (string, error) {
		return func(string) (string, error) { return v, err }
	}
	for _, c := range []struct {
		name, tz, link, want string
		err                  error
	}{
		{"TZ", "Europe/Lisbon", "", "Europe/Lisbon", nil},
		{"TZ with a colon prefix", ":Europe/Lisbon", "", "Europe/Lisbon", nil},
		{"a bad TZ falls to the link", "Nowhere/Land", "/usr/share/zoneinfo/Europe/Paris", "Europe/Paris", nil},
		{"the link", "", "../usr/share/zoneinfo/America/New_York", "America/New_York", nil},
		{"no link", "", "", "UTC", errors.New("no")},
		{"a link outside zoneinfo", "", "/etc/other", "UTC", nil},
	} {
		if got := zoneName(env(c.tz), link(c.link, c.err)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
