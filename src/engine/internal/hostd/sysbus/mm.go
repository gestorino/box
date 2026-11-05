package sysbus

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"

	"concierge/engine/internal/hostd"
)

const (
	mmDest     = "org.freedesktop.ModemManager1"
	mmPath     = dbus.ObjectPath("/org/freedesktop/ModemManager1")
	modemIface = "org.freedesktop.ModemManager1.Modem"
	gppIface   = modemIface + ".Modem3gpp"
)

type mm struct{ conn *dbus.Conn }

// NewMM reads the first modem ModemManager knows. Reads need no privilege.
func NewMM(conn *dbus.Conn) hostd.ModemManager { return &mm{conn: conn} }

func (m *mm) Modem(ctx context.Context) (hostd.ModemInfo, hostd.SIMInfo, error) {
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	err := m.conn.Object(mmDest, mmPath).CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects)
	if err != nil {
		return hostd.ModemInfo{}, hostd.SIMInfo{}, err
	}
	for _, ifaces := range objects {
		modem, ok := ifaces[modemIface]
		if !ok {
			continue
		}
		gpp := ifaces[gppIface]
		info := hostd.ModemInfo{
			State:        modemState(val[int32](modem["State"])),
			AccessTech:   accessTech(val[uint32](modem["AccessTechnologies"])),
			Operator:     val[string](gpp["OperatorName"]),
			Registration: registration(val[uint32](gpp["RegistrationState"])),
		}
		if q, ok := modem["SignalQuality"].Value().([]any); ok && len(q) == 2 {
			s, _ := q[0].(uint32)
			info.Signal = int(s)
		}
		simPath := val[dbus.ObjectPath](modem["Sim"])
		sim := hostd.SIMInfo{Present: simPath != "" && simPath != "/", Lock: lock(val[uint32](modem["UnlockRequired"]))}
		retries, _ := modem["UnlockRetries"].Value().(map[uint32]uint32)
		sim.PINRetries, sim.PUKRetries = int(retries[2]), int(retries[4]) // MM_MODEM_LOCK_SIM_PIN, _SIM_PUK
		return info, sim, nil
	}
	return hostd.ModemInfo{}, hostd.SIMInfo{}, fmt.Errorf("no modem")
}

func val[T any](v dbus.Variant) T {
	t, _ := v.Value().(T)
	return t
}

func modemState(s int32) string {
	names := map[int32]string{-1: "failed", 0: "unknown", 1: "initializing", 2: "locked", 3: "disabled",
		4: "disabling", 5: "enabling", 6: "enabled", 7: "searching", 8: "registered", 9: "disconnecting",
		10: "connecting", 11: "connected"}
	if n, ok := names[s]; ok {
		return n
	}
	return "unknown"
}

// accessTech names the best technology in MM's bitmask.
func accessTech(t uint32) string {
	switch {
	case t&0x8000 != 0:
		return "5g"
	case t&0x4000 != 0:
		return "lte"
	case t&0x3e0 != 0: // UMTS, HSDPA, HSUPA, HSPA, HSPA+
		return "3g"
	case t&0x1e != 0: // GSM, GSM compact, GPRS, EDGE
		return "2g"
	}
	return "unknown"
}

func registration(r uint32) string {
	switch r {
	case 1:
		return "home"
	case 5:
		return "roaming"
	case 2:
		return "searching"
	case 3:
		return "denied"
	case 0:
		return "idle"
	}
	return "unknown"
}

// lock: only the SIM PIN and PUK stop the card working; PIN2 guards other things and is ignored.
func lock(l uint32) string {
	switch l {
	case 2:
		return "pin"
	case 4:
		return "puk"
	}
	return "none"
}
