package calls

import "testing"

func TestATReply(t *testing.T) {
	for buf, want := range map[string]string{
		"":                         "wait",
		"AT+QPCMV=1,2\r\r\n":       "wait",
		"AT+QPCMV=1,2\r\r\nOK\r\n": "ok",
		"\r\nOK\r\n":               "ok",
		"\r\nERROR\r\n":            "error",
		"\r\n+CME ERROR: 903\r\n":  "error",
		"\r\nOKAY\r\n":             "wait",
	} {
		done, err := atReply([]byte(buf))
		got := "wait"
		switch {
		case done && err == nil:
			got = "ok"
		case done:
			got = "error"
		}
		if got != want {
			t.Errorf("%q: %s, want %s", buf, got, want)
		}
	}
}

// The AT port is touched for routing the call's audio and nothing else.
func TestATCommandsAreOnlyAudioRouting(t *testing.T) {
	if atRoute != "AT+QPCMV=1,2" || atUnroute != "AT+QPCMV=0" {
		t.Fatalf("%q %q", atRoute, atUnroute)
	}
}
