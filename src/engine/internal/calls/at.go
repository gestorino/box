package calls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.bug.st/serial"
)

// The only two AT commands calld sends: the call's voice to the USB sound card, and back. Thin:
// verified on the box (infra/bkhd-1264/README.md, journal 12).
const (
	atRoute   = "AT+QPCMV=1,2"
	atUnroute = "AT+QPCMV=0"
	// The one query: the modem's temperatures, asked only while no call is live (metrics.go).
	atTemp = "AT+QTEMP"
	// The EC25's spare AT port: ModemManager uses if02 (infra/bkhd-1264/post-install.sh).
	atPortGlob = "/dev/serial/by-id/usb-Quectel_EC25*-if03-port0"
	atTimeout  = 5 * time.Second
)

// atReply reads what the modem has answered so far: done once a final line OK or ERROR (or a
// +CME ERROR) arrived, with the error in the second case.
func atReply(buf []byte) (done bool, err error) {
	for _, line := range strings.Split(string(buf), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "OK":
			return true, nil
		case line == "ERROR", strings.HasPrefix(line, "+CME ERROR"):
			return true, fmt.Errorf("modem answered %q", line)
		}
	}
	return false, nil
}

// atText is the reply's text before its final OK: the command's echo and the blank lines dropped.
func atText(buf []byte) string {
	var out []string
	for _, line := range strings.Split(string(buf), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "OK" || strings.HasPrefix(line, "AT") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// parseQTEMP reads the EC25's "+QTEMP: <pmic>,<xo>,<pa>" in degrees Celsius.
func parseQTEMP(reply string) (pmic, xo, pa float64, err error) {
	_, rest, ok := strings.Cut(reply, "+QTEMP:")
	fields := strings.Split(strings.TrimSpace(rest), ",")
	if !ok || len(fields) != 3 {
		return 0, 0, 0, fmt.Errorf("unexpected temperature reply %q", reply)
	}
	var v [3]float64
	for i, f := range fields {
		if v[i], err = strconv.ParseFloat(strings.TrimSpace(f), 64); err != nil {
			return 0, 0, 0, fmt.Errorf("unexpected temperature reply %q", reply)
		}
	}
	return v[0], v[1], v[2], nil
}

// at sends one command on the spare AT port and waits for its final answer.
func at(ctx context.Context, command string) error {
	_, err := atQuery(ctx, command)
	return err
}

// atQuery is at, returning the reply's text before the final OK.
func atQuery(ctx context.Context, command string) (string, error) {
	ports, _ := filepath.Glob(atPortGlob)
	if len(ports) == 0 {
		return "", errors.New("no EC25 AT port")
	}
	port, err := serial.Open(ports[0], &serial.Mode{BaudRate: 115200})
	if err != nil {
		return "", err
	}
	defer port.Close()
	if err := port.ResetInputBuffer(); err != nil {
		return "", err
	}
	if err := port.SetReadTimeout(200 * time.Millisecond); err != nil {
		return "", err
	}
	if _, err := port.Write([]byte(command + "\r")); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	chunk := make([]byte, 256)
	for end := time.Now().Add(atTimeout); time.Now().Before(end) && ctx.Err() == nil; {
		n, err := port.Read(chunk)
		if err != nil {
			return "", err
		}
		buf.Write(chunk[:n])
		if done, err := atReply(buf.Bytes()); done {
			return atText(buf.Bytes()), err
		}
	}
	return "", fmt.Errorf("no answer to %s", command)
}

// ModemTemperature asks the modem for its temperatures: the reply to AT+QTEMP.
func ModemTemperature(ctx context.Context) (string, error) { return atQuery(ctx, atTemp) }
