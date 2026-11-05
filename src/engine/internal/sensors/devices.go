package sensors

import (
	"os"
)

// IOPort is /dev/port: one byte at the offset that is the port number.
type IOPort struct{ f *os.File }

// OpenPort opens /dev/port read-write.
func OpenPort(path string) (*IOPort, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &IOPort{f}, nil
}

func (p *IOPort) WriteByteAt(addr int64, b byte) error {
	_, err := p.f.WriteAt([]byte{b}, addr)
	return err
}

func (p *IOPort) ReadByteAt(addr int64) (byte, error) {
	var b [1]byte
	if _, err := p.f.ReadAt(b[:], addr); err != nil {
		return 0, err
	}
	return b[0], nil
}

func (p *IOPort) Close() error { return p.f.Close() }

// CPUMSR is /dev/cpu/0/msr: eight bytes at the register's offset.
type CPUMSR struct{ f *os.File }

// OpenMSR opens the MSR device read-only.
func OpenMSR(path string) (*CPUMSR, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &CPUMSR{f}, nil
}

func (m *CPUMSR) Read(reg int64) (uint64, error) {
	var b [8]byte
	if _, err := m.f.ReadAt(b[:], reg); err != nil {
		return 0, err
	}
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v, nil
}

func (m *CPUMSR) Close() error { return m.f.Close() }
