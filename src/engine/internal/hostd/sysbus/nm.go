// Package sysbus implements hostd's ports over the system D-Bus: NetworkManager and ModemManager.
// It is a direct translation of each port method into D-Bus calls, kept thin on purpose — the
// logic lives in package hostd, where it is tested; this is verified on the box.
package sysbus

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"

	"concierge/engine/internal/hostd"
)

const (
	nmDest    = "org.freedesktop.NetworkManager"
	nmPath    = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	nmIface   = "org.freedesktop.NetworkManager"
	settings  = dbus.ObjectPath("/org/freedesktop/NetworkManager/Settings")
	devIface  = nmIface + ".Device"
	wifiIface = devIface + ".Wireless"
	apIface   = nmIface + ".AccessPoint"
	activeIf  = nmIface + ".Connection.Active"
	setIface  = nmIface + ".Settings"
	connIface = setIface + ".Connection"
	ip4Iface  = nmIface + ".IP4Config"
	modemType = 8 // NM_DEVICE_TYPE_MODEM
	activated = 100
	failed    = 120
	acActive  = 2 // NM_ACTIVE_CONNECTION_STATE_ACTIVATED
)

// Checkpoint flags: DELETE_NEW_CONNECTIONS. (DISCONNECT_NEW_DEVICES is refused by NetworkManager on
// a checkpoint that names devices, and a one-device checkpoint has no new devices to disconnect.)
const checkpointFlags = 0x2

type nm struct {
	conn      *dbus.Conn
	wifi      string // interface name, e.g. wlo1
	modemConn string // connection id, e.g. meo-lte
}

// NewNM talks to NetworkManager about the Wi-Fi interface wifi and the mobile-data connection named
// modemConn.
func NewNM(conn *dbus.Conn, wifi, modemConn string) hostd.NetworkManager {
	return &nm{conn: conn, wifi: wifi, modemConn: modemConn}
}

func (n *nm) obj(path dbus.ObjectPath) dbus.BusObject { return n.conn.Object(nmDest, path) }

func prop[T any](o dbus.BusObject, name string) (T, error) {
	var v T
	p, err := o.GetProperty(name)
	if err != nil {
		return v, err
	}
	return v, p.Store(&v)
}

// Uplink reads NetworkManager's primary connection — the one holding the default route.
func (n *nm) Uplink(ctx context.Context) (string, error) {
	primary, err := prop[dbus.ObjectPath](n.obj(nmPath), nmIface+".PrimaryConnection")
	if err != nil {
		return "", err
	}
	if primary == "/" {
		return "none", nil
	}
	typ, err := prop[string](n.obj(primary), activeIf+".Type")
	if err != nil {
		return "", err
	}
	switch typ {
	case "802-11-wireless":
		return "wifi", nil
	case "gsm":
		return "mobile", nil
	case "802-3-ethernet":
		return "wired", nil
	}
	return "none", nil
}

func (n *nm) device(ctx context.Context) (dbus.ObjectPath, error) {
	var dev dbus.ObjectPath
	err := n.obj(nmPath).CallWithContext(ctx, nmIface+".GetDeviceByIpIface", 0, n.wifi).Store(&dev)
	return dev, err
}

func (n *nm) Wifi(ctx context.Context) (hostd.WifiStatus, error) {
	var st hostd.WifiStatus
	radio, err := prop[bool](n.obj(nmPath), nmIface+".WirelessEnabled")
	if err != nil {
		return st, err
	}
	dev, err := n.device(ctx)
	if err != nil {
		return st, err
	}
	state, err := prop[uint32](n.obj(dev), devIface+".State")
	if err != nil {
		return st, err
	}
	st.Radio, st.State = radio, deviceState(state)

	activeUUID := ""
	if ac, err := prop[dbus.ObjectPath](n.obj(dev), devIface+".ActiveConnection"); err == nil && ac != "/" {
		activeUUID, _ = prop[string](n.obj(ac), activeIf+".Uuid")
	}
	if ap, err := prop[dbus.ObjectPath](n.obj(dev), wifiIface+".ActiveAccessPoint"); err == nil && ap != "/" && state == activated {
		ssid, _ := prop[[]byte](n.obj(ap), apIface+".Ssid")
		strength, _ := prop[byte](n.obj(ap), apIface+".Strength")
		freq, _ := prop[uint32](n.obj(ap), apIface+".Frequency")
		st.Active = &hostd.ActiveWifi{SSID: string(ssid), Signal: int(strength), FrequencyMHz: int(freq), IP4: n.ip4(dev, devIface)}
	}

	conns, err := n.connections(ctx)
	if err != nil {
		return st, err
	}
	st.Saved = []hostd.SavedNetwork{}
	for _, c := range conns {
		if str(c.settings, "connection", "type") != "802-11-wireless" {
			continue
		}
		ssid, _ := c.settings["802-11-wireless"]["ssid"].Value().([]byte)
		id := str(c.settings, "connection", "uuid")
		st.Saved = append(st.Saved, hostd.SavedNetwork{ID: id, SSID: string(ssid), Active: id == activeUUID})
	}
	return st, nil
}

// ip4 reads the first IPv4 address of an object with an Ip4Config property: a device (iface
// devIface) or an active connection (activeIf).
func (n *nm) ip4(owner dbus.ObjectPath, iface string) string {
	cfg, err := prop[dbus.ObjectPath](n.obj(owner), iface+".Ip4Config")
	if err != nil || cfg == "/" {
		return ""
	}
	data, err := prop[[]map[string]dbus.Variant](n.obj(cfg), ip4Iface+".AddressData")
	if err != nil || len(data) == 0 {
		return ""
	}
	addr, _ := data[0]["address"].Value().(string)
	return addr
}

func (n *nm) Scan(ctx context.Context) ([]hostd.VisibleNetwork, error) {
	dev, err := n.device(ctx)
	if err != nil {
		return nil, err
	}
	before, _ := prop[int64](n.obj(dev), wifiIface+".LastScan")
	// A refused request (scanned very recently, or busy) still leaves a usable list.
	if n.obj(dev).CallWithContext(ctx, wifiIface+".RequestScan", 0, map[string]dbus.Variant{}).Err == nil {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if last, _ := prop[int64](n.obj(dev), wifiIface+".LastScan"); last != before {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	var aps []dbus.ObjectPath
	if err := n.obj(dev).CallWithContext(ctx, wifiIface+".GetAllAccessPoints", 0).Store(&aps); err != nil {
		return nil, err
	}
	out := make([]hostd.VisibleNetwork, 0, len(aps))
	for _, ap := range aps {
		o := n.obj(ap)
		ssid, _ := prop[[]byte](o, apIface+".Ssid")
		strength, _ := prop[byte](o, apIface+".Strength")
		flags, _ := prop[uint32](o, apIface+".Flags")
		wpa, _ := prop[uint32](o, apIface+".WpaFlags")
		rsn, _ := prop[uint32](o, apIface+".RsnFlags")
		out = append(out, hostd.VisibleNetwork{SSID: string(ssid), Signal: int(strength), Security: security(flags, wpa, rsn)})
	}
	return out, nil
}

func security(flags, wpa, rsn uint32) string {
	switch {
	case rsn&0x400 != 0: // key management SAE
		return "wpa3"
	case wpa|rsn != 0:
		return "wpa"
	case flags&0x1 != 0: // privacy without WPA: WEP
		return "wep"
	}
	return "open"
}

func (n *nm) AddWifi(ctx context.Context, w hostd.NewWifi) (string, error) {
	uuid := newUUID()
	s := map[string]map[string]dbus.Variant{
		"connection": {
			"id": dbus.MakeVariant(w.SSID), "uuid": dbus.MakeVariant(uuid),
			"type": dbus.MakeVariant("802-11-wireless"), "autoconnect": dbus.MakeVariant(w.Autoconnect),
			"autoconnect-priority": dbus.MakeVariant(w.Priority),
		},
		"802-11-wireless": {"ssid": dbus.MakeVariant([]byte(w.SSID)), "mode": dbus.MakeVariant("infrastructure")},
		"ipv4":            {"method": dbus.MakeVariant("auto")},
		"ipv6":            {"method": dbus.MakeVariant("auto")},
	}
	if w.Password != "" {
		s["802-11-wireless-security"] = map[string]dbus.Variant{
			"key-mgmt": dbus.MakeVariant("wpa-psk"), "psk": dbus.MakeVariant(w.Password),
		}
	}
	flags := uint32(0x1) // TO_DISK
	if w.InMemory {
		flags = 0x2 | 0x20 // IN_MEMORY | BLOCK_AUTOCONNECT
	}
	var path dbus.ObjectPath
	var result map[string]dbus.Variant
	err := n.obj(settings).CallWithContext(ctx, setIface+".AddConnection2", 0, s, flags, map[string]dbus.Variant{}).Store(&path, &result)
	return uuid, err
}

func (n *nm) byUUID(ctx context.Context, uuid string) (dbus.ObjectPath, error) {
	var p dbus.ObjectPath
	err := n.obj(settings).CallWithContext(ctx, setIface+".GetConnectionByUuid", 0, uuid).Store(&p)
	return p, err
}

func (n *nm) ActivateWifi(ctx context.Context, uuid string, wait time.Duration) (hostd.Activation, error) {
	conn, err := n.byUUID(ctx, uuid)
	if err != nil {
		return hostd.Activation{}, err
	}
	dev, err := n.device(ctx)
	if err != nil {
		return hostd.Activation{}, err
	}
	var changes <-chan stateChange
	if wait > 0 {
		// Subscribe before activating, so no transition is missed.
		c, stop, err := n.stateChanges(dev)
		if err != nil {
			return hostd.Activation{}, err
		}
		defer stop()
		changes = c
	}
	var active dbus.ObjectPath
	if err := n.obj(nmPath).CallWithContext(ctx, nmIface+".ActivateConnection", 0, conn, dev, dbus.ObjectPath("/")).Store(&active); err != nil {
		return hostd.Activation{}, err
	}
	if wait == 0 {
		return hostd.Activation{}, nil
	}
	if ok, reason := awaitActivation(ctx, changes, wait); !ok {
		return hostd.Activation{Reason: reason}, nil
	}
	return hostd.Activation{Connected: true, IP4: n.ip4(dev, devIface)}, nil
}

// stateChanges streams a device's StateChanged signals until stop is called.
func (n *nm) stateChanges(dev dbus.ObjectPath) (<-chan stateChange, func(), error) {
	match := []dbus.MatchOption{
		dbus.WithMatchObjectPath(dev), dbus.WithMatchInterface(devIface), dbus.WithMatchMember("StateChanged"),
	}
	if err := n.conn.AddMatchSignal(match...); err != nil {
		return nil, nil, err
	}
	raw := make(chan *dbus.Signal, 32)
	n.conn.Signal(raw)
	out := make(chan stateChange, 32)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-raw:
				if sig.Path != dev || sig.Name != devIface+".StateChanged" || len(sig.Body) != 3 {
					continue
				}
				state, _ := sig.Body[0].(uint32)
				reason, _ := sig.Body[2].(uint32)
				select {
				case out <- stateChange{state, reason}:
				default: // nobody is reading any more; drop rather than block the bus
				}
			case <-done:
				return
			}
		}
	}()
	stop := func() {
		close(done)
		n.conn.RemoveSignal(raw)
		n.conn.RemoveMatchSignal(match...)
	}
	return out, stop, nil
}

func (n *nm) DeleteConnection(ctx context.Context, uuid string) error {
	conn, err := n.byUUID(ctx, uuid)
	if err != nil {
		return err
	}
	return n.obj(conn).CallWithContext(ctx, connIface+".Delete", 0).Err
}

type connection struct {
	path     dbus.ObjectPath
	settings map[string]map[string]dbus.Variant
}

// connections lists every saved connection's settings — without secrets: GetSecrets is never called.
func (n *nm) connections(ctx context.Context) ([]connection, error) {
	var paths []dbus.ObjectPath
	if err := n.obj(settings).CallWithContext(ctx, setIface+".ListConnections", 0).Store(&paths); err != nil {
		return nil, err
	}
	out := make([]connection, 0, len(paths))
	for _, p := range paths {
		var s map[string]map[string]dbus.Variant
		if err := n.obj(p).CallWithContext(ctx, connIface+".GetSettings", 0).Store(&s); err != nil {
			return nil, err
		}
		out = append(out, connection{path: p, settings: s})
	}
	return out, nil
}

func str(s map[string]map[string]dbus.Variant, group, key string) string {
	v, _ := s[group][key].Value().(string)
	return v
}

func (n *nm) modem(ctx context.Context) (connection, error) {
	conns, err := n.connections(ctx)
	if err != nil {
		return connection{}, err
	}
	for _, c := range conns {
		if str(c.settings, "connection", "id") == n.modemConn {
			return c, nil
		}
	}
	return connection{}, fmt.Errorf("no connection named %q", n.modemConn)
}

// activeFor is the active connection for a saved connection's UUID, or "".
func (n *nm) activeFor(uuid string) dbus.ObjectPath {
	actives, _ := prop[[]dbus.ObjectPath](n.obj(nmPath), nmIface+".ActiveConnections")
	for _, a := range actives {
		if u, _ := prop[string](n.obj(a), activeIf+".Uuid"); u == uuid {
			return a
		}
	}
	return ""
}

func (n *nm) CellularData(ctx context.Context) (hostd.DataInfo, error) {
	c, err := n.modem(ctx)
	if err != nil {
		return hostd.DataInfo{}, err
	}
	d := hostd.DataInfo{Enabled: true, State: "disconnected", APN: str(c.settings, "gsm", "apn")}
	if v, ok := c.settings["connection"]["autoconnect"].Value().(bool); ok {
		d.Enabled = v
	}
	if a := n.activeFor(str(c.settings, "connection", "uuid")); a != "" {
		switch state, _ := prop[uint32](n.obj(a), activeIf+".State"); state {
		case acActive:
			d.State, d.IP4 = "connected", n.ip4(a, activeIf)
		case 1:
			d.State = "connecting"
		}
	}
	return d, nil
}

func (n *nm) UpdateCellular(ctx context.Context, u hostd.CellularUpdate) error {
	c, err := n.modem(ctx)
	if err != nil {
		return err
	}
	s := c.settings
	// The deprecated address/route forms come back alongside the current ones; sending both makes
	// Update refuse the connection.
	for _, ip := range []string{"ipv4", "ipv6"} {
		delete(s[ip], "addresses")
		delete(s[ip], "routes")
	}
	if u.APN != nil {
		if s["gsm"] == nil {
			s["gsm"] = map[string]dbus.Variant{}
		}
		s["gsm"]["apn"] = dbus.MakeVariant(*u.APN)
	}
	if u.DataEnabled != nil {
		s["connection"]["autoconnect"] = dbus.MakeVariant(*u.DataEnabled)
	}
	var result map[string]dbus.Variant
	if err := n.obj(c.path).CallWithContext(ctx, connIface+".Update2", 0, s, uint32(0x1), map[string]dbus.Variant{}).Store(&result); err != nil {
		return err
	}
	if u.DataEnabled == nil {
		return nil
	}
	active := n.activeFor(str(s, "connection", "uuid"))
	if !*u.DataEnabled {
		if active == "" {
			return nil
		}
		return n.obj(nmPath).CallWithContext(ctx, nmIface+".DeactivateConnection", 0, active).Err
	}
	if active != "" {
		return nil
	}
	var a dbus.ObjectPath
	return n.obj(nmPath).CallWithContext(ctx, nmIface+".ActivateConnection", 0, c.path, dbus.ObjectPath("/"), dbus.ObjectPath("/")).Store(&a)
}

func (n *nm) deviceFor(ctx context.Context, d hostd.Device) (dbus.ObjectPath, error) {
	if d == hostd.DeviceWifi {
		return n.device(ctx)
	}
	var devs []dbus.ObjectPath
	if err := n.obj(nmPath).CallWithContext(ctx, nmIface+".GetDevices", 0).Store(&devs); err != nil {
		return "", err
	}
	for _, dev := range devs {
		if t, _ := prop[uint32](n.obj(dev), devIface+".DeviceType"); t == modemType {
			return dev, nil
		}
	}
	return "", fmt.Errorf("no modem device")
}

func (n *nm) CheckpointCreate(ctx context.Context, d hostd.Device, rollback time.Duration) (string, error) {
	dev, err := n.deviceFor(ctx, d)
	if err != nil {
		return "", err
	}
	var cp dbus.ObjectPath
	err = n.obj(nmPath).CallWithContext(ctx, nmIface+".CheckpointCreate", 0,
		[]dbus.ObjectPath{dev}, uint32(rollback/time.Second), uint32(checkpointFlags)).Store(&cp)
	return string(cp), err
}

func (n *nm) CheckpointDestroy(ctx context.Context, id string) error {
	return n.obj(nmPath).CallWithContext(ctx, nmIface+".CheckpointDestroy", 0, dbus.ObjectPath(id)).Err
}

func (n *nm) CheckpointRollback(ctx context.Context, id string) error {
	var result map[string]uint32
	return n.obj(nmPath).CallWithContext(ctx, nmIface+".CheckpointRollback", 0, dbus.ObjectPath(id)).Store(&result)
}

func deviceState(s uint32) string {
	switch {
	case s == activated:
		return "connected"
	case s >= 40 && s < activated:
		return "connecting"
	case s == 30 || s == 110 || s == failed:
		return "disconnected"
	case s == 20:
		return "unavailable"
	}
	return "unmanaged"
}

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
