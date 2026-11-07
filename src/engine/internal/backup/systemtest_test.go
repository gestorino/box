package backup

import "strings"

// fakeSystem is a System that runs nothing: it records every call in order
// ("Method arg ...") and answers with what a test set.
type fakeSystem struct {
	calls []string

	key           []byte // what Decrypt returns
	lastEncrypted []byte // what Encrypt was given (never in calls)
	decryptErr    error
	encryptErr    error
	callsOut      string
	callsErr      error
	active        map[string]bool // a unit missing from it is active
	stopErr       error
	startErr      error
	maskErr       error
	unmaskErr     error
	syncErr       error
}

func (f *fakeSystem) record(method string, args ...string) {
	f.calls = append(f.calls, strings.TrimSpace(method+" "+strings.Join(args, " ")))
}

func (f *fakeSystem) Stop(units []string) error {
	f.record("Stop", units...)
	return f.stopErr
}

func (f *fakeSystem) Start(units []string) error {
	f.record("Start", units...)
	return f.startErr
}

func (f *fakeSystem) Mask(units []string) error {
	f.record("Mask", units...)
	return f.maskErr
}

func (f *fakeSystem) Unmask(units []string) error {
	f.record("Unmask", units...)
	return f.unmaskErr
}

func (f *fakeSystem) Active(unit string) (bool, error) {
	f.record("Active", unit)
	if a, ok := f.active[unit]; ok {
		return a, nil
	}
	return true, nil
}

func (f *fakeSystem) Decrypt(credPath string) ([]byte, error) {
	f.record("Decrypt", credPath)
	return f.key, f.decryptErr
}

// Encrypt records the path only, never the key.
func (f *fakeSystem) Encrypt(key []byte, credPath string) error {
	f.record("Encrypt", credPath)
	f.lastEncrypted = key
	return f.encryptErr
}

func (f *fakeSystem) Calls() (string, error) {
	f.record("Calls")
	return f.callsOut, f.callsErr
}

func (f *fakeSystem) SyncVenv(browserDir string) error {
	f.record("SyncVenv", browserDir)
	return f.syncErr
}

var _ System = (*fakeSystem)(nil)
