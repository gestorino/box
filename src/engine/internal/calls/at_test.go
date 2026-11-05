package calls

import (
	"context"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

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

// exclusivePort stands in for the EC25's AT port as go.bug.st/serial opens it (TIOCEXCL): a second
// open while one is held fails with EBUSY. It answers OK after a short wait.
type exclusivePort struct {
	held  *atomic.Bool
	wrote []byte
	reads int
}

func (p *exclusivePort) ResetInputBuffer() error            { return nil }
func (p *exclusivePort) SetReadTimeout(time.Duration) error { return nil }
func (p *exclusivePort) Write(b []byte) (int, error) {
	p.wrote = append(p.wrote, b...)
	return len(b), nil
}
func (p *exclusivePort) Close() error { p.held.Store(false); return nil }
func (p *exclusivePort) Read(b []byte) (int, error) {
	p.reads++
	if p.reads == 1 {
		time.Sleep(50 * time.Millisecond) // the modem thinking
		return 0, nil
	}
	return copy(b, "\r\nOK\r\n"), nil
}

// Every use of the AT port waits for the one before it, so a call's Route never fails because a
// temperature query holds the port.
func TestATUsesSerialise(t *testing.T) {
	var held atomic.Bool
	defer func(open func() (atPort, error)) { openATPort = open }(openATPort)
	openATPort = func() (atPort, error) {
		if !held.CompareAndSwap(false, true) {
			return nil, syscall.EBUSY
		}
		return &exclusivePort{held: &held}, nil
	}
	errs := make(chan error, 2)
	go func() { _, err := ModemTemperature(context.Background()); errs <- err }()
	go func() { errs <- modemAudio{}.Route(context.Background()) }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("a concurrent AT use failed: %v", err)
		}
	}
}
