package backup

import (
	"bytes"
	"strings"
)

// fakeSystem is a System that runs nothing: it records every call in order
// ("Method arg ...") and answers with what a test set.
type fakeSystem struct {
	calls []string

	key           []byte // what Decrypt returns
	lastEncrypted []byte // what Encrypt was given (never in calls)
	decryptErr    error
	encryptErr    error
	callsOut      string
	callsSeq      []string // when set, each Calls answers the next one (then callsOut)
	callsErr      error
	active        map[string]bool // a unit missing from it is active
	stopErr       error
	startErr      error
	syncErr       error
	onStart       func() // called on Start, before it answers
	onStop        func() // called on Stop, before it answers

	holds   int      // Hold calls
	holding bool     // between a Hold and its release
	held    []string // the calls made while holding
}

func (f *fakeSystem) record(method string, args ...string) {
	c := strings.TrimSpace(method + " " + strings.Join(args, " "))
	f.calls = append(f.calls, c)
	if f.holding {
		f.held = append(f.held, c)
	}
}

func (f *fakeSystem) Hold() (release func()) {
	f.holds++
	f.holding = true
	return func() { f.holding = false }
}

func (f *fakeSystem) Stop(units []string) error {
	f.record("Stop", units...)
	if f.onStop != nil {
		f.onStop()
	}
	return f.stopErr
}

func (f *fakeSystem) Start(units []string) error {
	f.record("Start", units...)
	if f.onStart != nil {
		f.onStart()
	}
	return f.startErr
}

func (f *fakeSystem) Active(unit string) (bool, error) {
	f.record("Active", unit)
	if a, ok := f.active[unit]; ok {
		return a, nil
	}
	return true, nil
}

// Decrypt hands out a copy, fresh memory as the real one's is: callers wipe the key they get.
func (f *fakeSystem) Decrypt(credPath string) ([]byte, error) {
	f.record("Decrypt", credPath)
	return bytes.Clone(f.key), f.decryptErr
}

// Encrypt records the path only, never the key; it keeps a copy (callers wipe theirs).
func (f *fakeSystem) Encrypt(key []byte, credPath string) error {
	f.record("Encrypt", credPath)
	f.lastEncrypted = bytes.Clone(key)
	return f.encryptErr
}

func (f *fakeSystem) Calls() (string, error) {
	f.record("Calls")
	if len(f.callsSeq) > 0 {
		out := f.callsSeq[0]
		f.callsSeq = f.callsSeq[1:]
		return out, f.callsErr
	}
	return f.callsOut, f.callsErr
}

func (f *fakeSystem) SyncVenv(browserDir string) error {
	f.record("SyncVenv", browserDir)
	return f.syncErr
}

var _ System = (*fakeSystem)(nil)
