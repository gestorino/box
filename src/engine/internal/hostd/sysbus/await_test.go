package sysbus

import (
	"context"
	"testing"
	"time"
)

func feed(changes ...stateChange) <-chan stateChange {
	ch := make(chan stateChange, len(changes))
	for _, c := range changes {
		ch <- c
	}
	return ch
}

// The reason must come from the transition into FAILED itself: NetworkManager resets the device's
// StateReason on the next state, and autoconnect follows at once.
func TestAwaitActivationTakesTheReasonFromTheFailure(t *testing.T) {
	cases := []struct {
		name    string
		changes []stateChange
		want    string
	}{
		{"wrong password", []stateChange{{110, 0}, {30, 0}, {40, 0}, {50, 0}, {60, 0}, {120, 7}, {30, 0}, {100, 0}}, "wrong_password"},
		{"supplicant timeout", []stateChange{{40, 0}, {120, 11}}, "wrong_password"},
		{"not found", []stateChange{{40, 0}, {120, 53}}, "not_found"},
		{"no address", []stateChange{{40, 0}, {70, 0}, {120, 17}}, "no_address"},
		{"other", []stateChange{{40, 0}, {120, 1}}, "unavailable"},
	}
	for _, c := range cases {
		connected, reason := awaitActivation(context.Background(), feed(c.changes...), time.Second)
		if connected || reason != c.want {
			t.Errorf("%s: got (%t, %q), want (false, %q)", c.name, connected, reason, c.want)
		}
	}
}

func TestAwaitActivationConnects(t *testing.T) {
	connected, reason := awaitActivation(context.Background(), feed(stateChange{110, 0}, stateChange{40, 0}, stateChange{100, 0}), time.Second)
	if !connected || reason != "" {
		t.Fatalf("got (%t, %q)", connected, reason)
	}
}

func TestAwaitActivationTimesOut(t *testing.T) {
	connected, reason := awaitActivation(context.Background(), feed(stateChange{40, 0}), 20*time.Millisecond)
	if connected || reason != "timeout" {
		t.Fatalf("got (%t, %q)", connected, reason)
	}
}

// NetworkManager refuses DISCONNECT_NEW_DEVICES (0x4) on a checkpoint that names devices — and
// every hostd checkpoint names exactly one. Found on the box: "the DISCONNECT_NEW_DEVICES flag can
// only be used with an empty device list".
func TestCheckpointFlagsSuitADeviceList(t *testing.T) {
	if checkpointFlags&0x4 != 0 {
		t.Fatalf("checkpointFlags %#x includes DISCONNECT_NEW_DEVICES", checkpointFlags)
	}
	if checkpointFlags&0x2 == 0 {
		t.Fatalf("checkpointFlags %#x must keep DELETE_NEW_CONNECTIONS", checkpointFlags)
	}
}
