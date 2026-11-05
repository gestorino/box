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

// The AT port is touched for routing the call's audio and reading the temperature, nothing else.
func TestATCommandsAreOnlyAudioRoutingAndTemperature(t *testing.T) {
	if atRoute != "AT+QPCMV=1,2" || atUnroute != "AT+QPCMV=0" || atTemp != "AT+QTEMP" {
		t.Fatalf("%q %q %q", atRoute, atUnroute, atTemp)
	}
}

func TestParseQTEMP(t *testing.T) {
	pmic, xo, pa, err := parseQTEMP("\r\n+QTEMP: 34,35,33\r\n")
	if err != nil || pmic != 34 || xo != 35 || pa != 33 {
		t.Fatalf("%v %v %v %v", pmic, xo, pa, err)
	}
	for _, bad := range []string{"+QTEMP: x", "", "+QTEMP: 1,2", "34,35,33"} {
		if _, _, _, err := parseQTEMP(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestATText(t *testing.T) {
	if got := atText([]byte("AT+QTEMP\r\r\n+QTEMP: 34,35,33\r\n\r\nOK\r\n")); got != "+QTEMP: 34,35,33" {
		t.Fatalf("%q", got)
	}
}
