package hostd

import (
	"context"
	"time"
)

// Device names the device a checkpoint covers.
type Device int

const (
	DeviceWifi Device = iota
	DeviceModem
)

func (d Device) String() string {
	if d == DeviceModem {
		return "modem"
	}
	return "wifi"
}

// NetworkManager is everything hostd asks of NetworkManager. The D-Bus implementation lives in
// ./dbus; tests use an in-process fake. ActivateWifi with wait 0 returns once the activation is
// requested; with a wait it reports the outcome.
type NetworkManager interface {
	Wifi(ctx context.Context) (WifiStatus, error)
	Scan(ctx context.Context) ([]VisibleNetwork, error)
	AddWifi(ctx context.Context, w NewWifi) (id string, err error)
	ActivateWifi(ctx context.Context, id string, wait time.Duration) (Activation, error)
	DeleteConnection(ctx context.Context, id string) error
	// Uplink names the link the box's traffic leaves by: wifi, mobile, wired or none.
	Uplink(ctx context.Context) (string, error)
	CellularData(ctx context.Context) (DataInfo, error)
	UpdateCellular(ctx context.Context, u CellularUpdate) error
	CheckpointCreate(ctx context.Context, d Device, rollback time.Duration) (string, error)
	CheckpointDestroy(ctx context.Context, id string) error
	CheckpointRollback(ctx context.Context, id string) error
}

// ModemManager reads the modem and the SIM. Reads need no privilege. (SMS and calls are smsd's and
// calld's.)
type ModemManager interface {
	Modem(ctx context.Context) (ModemInfo, SIMInfo, error)
}
