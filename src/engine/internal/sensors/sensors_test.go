package sensors

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakePort struct {
	regs    map[byte]byte
	index   byte
	written []int64
}

func (f *fakePort) WriteByteAt(addr int64, b byte) error {
	f.written = append(f.written, addr)
	f.index = b
	return nil
}
func (f *fakePort) ReadByteAt(addr int64) (byte, error) { return f.regs[f.index], nil }

type fakeMSR map[int64]uint64

func (f fakeMSR) Read(reg int64) (uint64, error) {
	v, ok := f[reg]
	if !ok {
		return 0, errors.New("no such msr")
	}
	return v, nil
}

type deadMSR struct{}

func (deadMSR) Read(int64) (uint64, error) { return 0, errors.New("dead") }

func TestBoardTemps(t *testing.T) {
	p := &fakePort{regs: map[byte]byte{0x29: 44, 0x2A: 42, 0x2B: 38}}
	got, err := BoardTemps(p)
	if err != nil || got != [3]float64{44, 42, 38} {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, a := range p.written {
		if a != 0xa45 {
			t.Fatalf("wrote port 0x%x; only 0xa45 may be written", a)
		}
	}
}

func TestFanOutputs(t *testing.T) {
	p := &fakePort{regs: map[byte]byte{0x15: 0x02, 0x16: 0x81, 0x17: 0x81, 0x63: 0x00, 0x6B: 0x78, 0x73: 0x78}}
	got, err := FanOutputs(p)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != (FanOutput{1, false, 0}) {
		t.Fatalf("output 1: %+v", got[0])
	}
	if !got[1].Automatic || !got[2].Automatic || got[1].Output != 2 || got[2].ManualPWM != 120.0/255 {
		t.Fatalf("outputs 2, 3: %+v %+v", got[1], got[2])
	}
}

func TestPackageEnergy(t *testing.T) {
	j, err := PackageEnergyJoules(fakeMSR{0x606: 0x000A0E03, 0x611: 16384})
	if err != nil || j != 1.0 {
		t.Fatalf("got %v, %v", j, err)
	}
	j, _ = PackageEnergyJoules(fakeMSR{0x606: 0x000A0E03, 0x611: 1<<32 | 16384})
	if j != 1.0 {
		t.Fatalf("only the low 32 bits count, got %v", j)
	}
}

func TestCStateLimit(t *testing.T) {
	limit, locked, err := CStateLimit(fakeMSR{0xE2: 0x74008000})
	if err != nil || limit != 0 || !locked {
		t.Fatalf("got %d %v %v", limit, locked, err)
	}
}

func TestPowerLimits(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "constraint_0_power_limit_uw"), []byte("15000000\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "constraint_1_power_limit_uw"), []byte("20000000\n"), 0o600)
	l, s, err := PowerLimits(dir)
	if err != nil || l != 15 || s != 20 {
		t.Fatalf("got %v %v %v", l, s, err)
	}
}

func TestDirBytes(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(t.TempDir(), "big")
	os.WriteFile(big, make([]byte, 1000), 0o600)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o700)
	os.WriteFile(filepath.Join(dir, "a"), make([]byte, 10), 0o600)
	os.WriteFile(filepath.Join(dir, "sub", "b"), make([]byte, 20), 0o600)
	os.Symlink(big, filepath.Join(dir, "link"))
	n, err := DirBytes(dir)
	if err != nil || n != 30 {
		t.Fatalf("got %d, %v", n, err)
	}
}

func TestCollectorWritesAndKeepsStorage(t *testing.T) {
	rapl := t.TempDir()
	os.WriteFile(filepath.Join(rapl, "constraint_0_power_limit_uw"), []byte("15000000"), 0o600)
	os.WriteFile(filepath.Join(rapl, "constraint_1_power_limit_uw"), []byte("20000000"), 0o600)
	now := time.Unix(1000, 0)
	measured := 0
	c := &Collector{
		Port:         &fakePort{regs: map[byte]byte{0x29: 44, 0x2A: 42, 0x2B: 38, 0x15: 2, 0x16: 0x81, 0x17: 0x81}},
		MSR:          fakeMSR{0x606: 0x000A0E03, 0x611: 16384, 0xE2: 0x8000},
		RAPLDir:      rapl,
		Storage:      map[string]string{"engine": "/x"},
		StorageEvery: 5 * time.Minute,
		Now:          func() time.Time { return now },
		dirBytes:     func(string) (int64, error) { measured++; return 42, nil },
	}
	path := filepath.Join(t.TempDir(), "s.prom")
	read := func() string {
		if err := c.WriteTextfile(path); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(path)
		return string(b)
	}
	out := read()
	for _, name := range []string{
		`concierge_board_temp_celsius{sensor="1"} 44`,
		`concierge_fan_output_automatic{output="2"} 1`,
		`concierge_fan_output_manual_pwm_ratio{output="1"} 0`,
		`# TYPE concierge_package_energy_joules_total counter`,
		`concierge_package_energy_joules_total 1`,
		`concierge_power_limit_watts{limit="long_term"} 15`,
		`concierge_power_limit_watts{limit="short_term"} 20`,
		`concierge_package_cstate_limit 0`,
		`concierge_package_cstate_locked 1`,
		`concierge_storage_bytes{service="engine"} 42`,
	} {
		if !strings.Contains(out, name) {
			t.Errorf("missing %q in\n%s", name, out)
		}
	}
	now = now.Add(time.Minute)
	if out = read(); measured != 1 || !strings.Contains(out, `concierge_storage_bytes{service="engine"} 42`) {
		t.Fatalf("storage re-measured after 1 min (%d) or dropped:\n%s", measured, out)
	}
	now = now.Add(5 * time.Minute)
	read()
	if measured != 2 {
		t.Fatalf("storage not re-measured after 6 min: %d", measured)
	}

	c.MSR = deadMSR{}
	out = read()
	if !strings.Contains(out, "concierge_board_temp_celsius") || !strings.Contains(out, "concierge_storage_bytes") {
		t.Fatalf("a dead MSR must not take the rest down:\n%s", out)
	}
	if strings.Contains(out, "concierge_package_energy") {
		t.Fatalf("a failed source's series must be omitted:\n%s", out)
	}
}
