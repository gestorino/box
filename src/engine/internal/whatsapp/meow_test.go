package whatsapp

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
)

// The phone names a linked device by its platform type: UNKNOWN (whatsmeow's default) shows as
// "Other device"; a desktop client shows its OS name.
func TestDeviceShowsAsConcierge(t *testing.T) {
	identifyDevice()
	if store.DeviceProps.GetOs() != "Concierge" || store.DeviceProps.GetPlatformType() != waCompanionReg.DeviceProps_DESKTOP {
		t.Fatalf("os %q platform %v", store.DeviceProps.GetOs(), store.DeviceProps.GetPlatformType())
	}
}
