package calls

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	mmName     = "org.freedesktop.ModemManager1"
	mmPath     = dbus.ObjectPath("/org/freedesktop/ModemManager1")
	voiceIface = "org.freedesktop.ModemManager1.Modem.Voice"
	callIface  = "org.freedesktop.ModemManager1.Call"
)

// dbusModem is the Modem over ModemManager. Thin: verified on the box.
type dbusModem struct{ conn *dbus.Conn }

func NewDBusModem(conn *dbus.Conn) Modem { return &dbusModem{conn: conn} }

func (d *dbusModem) instance(ctx context.Context) (string, error) {
	var owner string
	err := d.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, mmName).Store(&owner)
	return owner, err
}

func (d *dbusModem) modem(ctx context.Context, instance string) (dbus.ObjectPath, error) {
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := d.conn.Object(instance, mmPath).CallWithContext(ctx, "org.freedesktop.DBus.ObjectManager.GetManagedObjects", 0).Store(&objects); err != nil {
		return "", err
	}
	for p, ifaces := range objects {
		if _, ok := ifaces[voiceIface]; ok {
			return p, nil
		}
	}
	return "", fmt.Errorf("no modem with voice")
}

func (d *dbusModem) List(ctx context.Context) (string, []ModemCall, error) {
	instance, err := d.instance(ctx)
	if err != nil {
		return "", nil, err
	}
	modem, err := d.modem(ctx, instance)
	if err != nil {
		return "", nil, err
	}
	v, err := d.conn.Object(instance, modem).GetProperty(voiceIface + ".Calls")
	if err != nil {
		return "", nil, err
	}
	paths, _ := v.Value().([]dbus.ObjectPath)
	out := make([]ModemCall, 0, len(paths))
	for _, p := range paths {
		var props map[string]dbus.Variant
		if err := d.conn.Object(instance, p).CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, callIface).Store(&props); err != nil {
			continue
		}
		number, _ := props["Number"].Value().(string)
		dir, _ := props["Direction"].Value().(int32)
		state, _ := props["State"].Value().(int32)
		reason, _ := props["StateReason"].Value().(int32)
		out = append(out, ModemCall{
			Path: string(p), Number: number,
			Direction: map[int32]string{1: "incoming", 2: "outgoing"}[dir],
			State: map[int32]string{1: "dialing", 2: "ringing-out", 3: "ringing-in", 4: "active", 5: "held",
				6: "waiting", 7: "terminated"}[state],
			Reason: map[int32]string{1: "outgoing-started", 2: "incoming-new", 3: "accepted", 4: "terminated",
				5: "refused-or-busy", 6: "error", 7: "audio-setup-failed", 8: "transferred", 9: "deflected"}[reason],
		})
	}
	return instance, out, nil
}

func (d *dbusModem) Delete(ctx context.Context, instance, path string) error {
	current, err := d.instance(ctx)
	if err != nil {
		return err
	}
	if current != instance {
		return ErrModemChanged
	}
	modem, err := d.modem(ctx, instance)
	if err != nil {
		return err
	}
	return d.conn.Object(instance, modem).CallWithContext(ctx, voiceIface+".DeleteCall", 0, dbus.ObjectPath(path)).Err
}

func (d *dbusModem) Dial(ctx context.Context, number string) (string, string, error) {
	instance, err := d.instance(ctx)
	if err != nil {
		return "", "", err
	}
	modem, err := d.modem(ctx, instance)
	if err != nil {
		return "", "", err
	}
	var path dbus.ObjectPath
	props := map[string]dbus.Variant{"number": dbus.MakeVariant(number)}
	if err := d.conn.Object(instance, modem).CallWithContext(ctx, voiceIface+".CreateCall", 0, props).Store(&path); err != nil {
		return "", "", err
	}
	if err := d.conn.Object(instance, path).CallWithContext(ctx, callIface+".Start", 0).Err; err != nil {
		d.conn.Object(instance, modem).CallWithContext(context.WithoutCancel(ctx), voiceIface+".DeleteCall", 0, path)
		return "", "", err
	}
	return instance, string(path), nil
}

func (d *dbusModem) Hangup(ctx context.Context, instance, path string) error {
	return d.conn.Object(instance, dbus.ObjectPath(path)).CallWithContext(ctx, callIface+".Hangup", 0).Err
}
