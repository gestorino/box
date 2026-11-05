// Package hostd is the host service: the only process allowed to change the box's network. It runs
// as its own user with exactly the NetworkManager permissions its operations need (polkit), and
// every change it makes runs under a NetworkManager checkpoint that undoes itself unless confirmed.
// Design: docs/superpowers/specs/2026-10-27-settings-network-design.md.
package hostd

// WifiStatus is the Wi-Fi device as the console shows it. State is one of connected, connecting,
// disconnected, unavailable, unmanaged.
type WifiStatus struct {
	Radio  bool           `json:"radio"`
	State  string         `json:"state"`
	Active *ActiveWifi    `json:"active,omitempty"`
	Saved  []SavedNetwork `json:"saved"`
}

type ActiveWifi struct {
	SSID         string `json:"ssid"`
	Signal       int    `json:"signal"`
	FrequencyMHz int    `json:"frequency_mhz"`
	IP4          string `json:"ip4,omitempty"`
}

// SavedNetwork is a stored Wi-Fi connection; ID is its NetworkManager UUID. Never carries secrets.
type SavedNetwork struct {
	ID     string `json:"id"`
	SSID   string `json:"ssid"`
	Active bool   `json:"active"`
}

// VisibleNetwork is one scan result. Security is open, wep, wpa or wpa3.
type VisibleNetwork struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"`
	Security string `json:"security"`
}

// Checkpoint is a pending change the owner can keep or undo. Kind is wifi-connect, wifi-forget or
// cellular.
type Checkpoint struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	RollbackInS int    `json:"rollback_in_s"`
}

type CellularUpdate struct {
	APN         *string `json:"apn,omitempty"`
	DataEnabled *bool   `json:"data_enabled,omitempty"`
}

type ModemInfo struct {
	State        string `json:"state"`
	Operator     string `json:"operator"`
	AccessTech   string `json:"access_tech"`
	Signal       int    `json:"signal"`
	Registration string `json:"registration"`
}

// SIMInfo: Present is a SIM in the modem; Lock is none, pin or puk.
type SIMInfo struct {
	Present    bool   `json:"present"`
	Lock       string `json:"lock"`
	PINRetries int    `json:"pin_retries"`
	PUKRetries int    `json:"puk_retries"`
}

type DataInfo struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	APN     string `json:"apn"`
	IP4     string `json:"ip4,omitempty"`
}

type CellularStatus struct {
	Modem ModemInfo `json:"modem"`
	SIM   SIMInfo   `json:"sim"`
	Data  DataInfo  `json:"data"`
}

// NewWifi is a connection to create. InMemory connections vanish on restart (used by a test).
type NewWifi struct {
	SSID, Password        string
	InMemory, Autoconnect bool
	Priority              int32
}

// Activation is the outcome of bringing a Wi-Fi connection up. Reason, when not connected, is one
// of wrong_password, not_found, no_address, timeout, unavailable.
type Activation struct {
	Connected bool
	IP4       string
	Reason    string
}
