// Package keyfile shows and vets the owner's API keys (the engine's OpenRouter key, calld's OpenAI
// key), which secretd keeps. A key never reaches a log, an error or an API answer but in its masked
// form.
package keyfile

import "strings"

// Masked is how the console shows a stored key: prefix when the key starts with it, "…", and the
// last four characters — none of a key too short to spare them.
func Masked(key, prefix string) string {
	m := "…"
	if strings.HasPrefix(key, prefix) {
		m = prefix + m
	}
	if len(key) >= 12 {
		m += key[len(key)-4:]
	}
	return m
}

// WellFormed is a key that can travel in a header: printable ASCII, no spaces, of a key's length.
func WellFormed(key string) bool {
	if key == "" || len(key) > 512 {
		return false
	}
	for i := range len(key) {
		if key[i] <= ' ' || key[i] > '~' {
			return false
		}
	}
	return true
}
