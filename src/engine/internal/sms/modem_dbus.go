package sms

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	mmName       = "org.freedesktop.ModemManager1"
	mmPath       = dbus.ObjectPath("/org/freedesktop/ModemManager1")
	modemIface   = "org.freedesktop.ModemManager1.Modem"
	messagingIfc = modemIface + ".Messaging"
	smsIface     = "org.freedesktop.ModemManager1.Sms"
)

// dbusModem is the Modem over ModemManager. Thin: verified on the box.
type dbusModem struct{ conn *dbus.Conn }

func NewDBusModem(conn *dbus.Conn) Modem { return &dbusModem{conn: conn} }

// instance is ModemManager's current unique bus name; it changes when ModemManager restarts.
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
		if _, ok := ifaces[messagingIfc]; ok {
			return p, nil
		}
	}
	return "", fmt.Errorf("no modem with messaging")
}

func (d *dbusModem) List(ctx context.Context) (string, []ModemSMS, error) {
	instance, err := d.instance(ctx)
	if err != nil {
		return "", nil, err
	}
	modem, err := d.modem(ctx, instance)
	if err != nil {
		return "", nil, err
	}
	v, err := d.conn.Object(instance, modem).GetProperty(messagingIfc + ".Messages")
	if err != nil {
		return "", nil, err
	}
	paths, _ := v.Value().([]dbus.ObjectPath)
	out := make([]ModemSMS, 0, len(paths))
	for _, p := range paths {
		var props map[string]dbus.Variant
		if err := d.conn.Object(instance, p).CallWithContext(ctx, "org.freedesktop.DBus.Properties.GetAll", 0, smsIface).Store(&props); err != nil {
			continue // deleted meanwhile
		}
		data, _ := props["Data"].Value().([]byte)
		out = append(out, ModemSMS{
			Path: string(p), Number: str(props["Number"]), Text: str(props["Text"]), Data: data,
			State: smsState(u32(props["State"])), PDUType: pduType(u32(props["PduType"])),
			Timestamp: smsTime(str(props["Timestamp"])), Reference: u32(props["MessageReference"]),
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
	return d.conn.Object(instance, modem).CallWithContext(ctx, messagingIfc+".Delete", 0, dbus.ObjectPath(path)).Err
}

// Send creates the message and sends it. A message that fails to send is deleted again: a "stored"
// draft left behind would never be archived nor cleared.
func (d *dbusModem) Send(ctx context.Context, number, text string) error {
	instance, err := d.instance(ctx)
	if err != nil {
		return err
	}
	modem, err := d.modem(ctx, instance)
	if err != nil {
		return err
	}
	var path dbus.ObjectPath
	props := map[string]dbus.Variant{"number": dbus.MakeVariant(number), "text": dbus.MakeVariant(text)}
	if err := d.conn.Object(instance, modem).CallWithContext(ctx, messagingIfc+".Create", 0, props).Store(&path); err != nil {
		return err
	}
	if err := d.conn.Object(instance, path).CallWithContext(ctx, smsIface+".Send", 0).Err; err != nil {
		d.conn.Object(instance, modem).CallWithContext(context.WithoutCancel(ctx), messagingIfc+".Delete", 0, path)
		return err
	}
	return nil
}

func str(v dbus.Variant) string { s, _ := v.Value().(string); return s }
func u32(v dbus.Variant) uint32 { n, _ := v.Value().(uint32); return n }

func smsState(s uint32) string {
	return map[uint32]string{1: "stored", 2: "receiving", 3: "received", 4: "sending", 5: "sent"}[s]
}

func pduType(p uint32) string {
	return map[uint32]string{1: "deliver", 2: "submit", 3: "status-report"}[p]
}

// smsTime normalises ModemManager's timestamp ("…+00" or "…+01:00") to RFC 3339 UTC, so times sort
// as strings; an unparseable one is passed through.
func smsTime(t string) string {
	for _, layout := range []string{"2006-01-02T15:04:05Z07", "2006-01-02T15:04:05Z07:00"} {
		if parsed, err := time.Parse(layout, t); err == nil {
			return parsed.UTC().Format(time.RFC3339)
		}
	}
	return strings.TrimSpace(t)
}
