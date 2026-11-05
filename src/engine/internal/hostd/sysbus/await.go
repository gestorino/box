package sysbus

import (
	"context"
	"time"
)

// stateChange is one NetworkManager Device.StateChanged signal: the new state and its reason.
type stateChange struct{ State, Reason uint32 }

// awaitActivation follows a device's state changes after an activation request until it is
// activated (100) or failed (120), or wait runs out. The reason is taken from the transition into
// FAILED itself — NetworkManager resets it on the very next state.
func awaitActivation(ctx context.Context, changes <-chan stateChange, wait time.Duration) (connected bool, reason string) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case c := <-changes:
			switch c.State {
			case activated:
				return true, ""
			case failed:
				return false, failureReason(c.Reason)
			}
		case <-timer.C:
			return false, "timeout"
		case <-ctx.Done():
			return false, "timeout"
		}
	}
}

func failureReason(reason uint32) string {
	switch reason {
	case 7, 8, 9, 10, 11: // no secrets, supplicant disconnect/config/failed/timeout
		return "wrong_password"
	case 53: // SSID not found
		return "not_found"
	case 5, 6, 15, 16, 17: // IP config unavailable/expired, DHCP start/error/failed
		return "no_address"
	}
	return "unavailable"
}
