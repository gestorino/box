// Package sensors reads the box's board, fan, power and storage figures for Prometheus's
// textfile collector. Hardware sits behind the Port and MSR interfaces, so everything here is
// tested with fakes; devices.go is the thin adapter, verified on the box.
package sensors

import "fmt"

const (
	ecIndex = 0xa45 // the IT8784's index port: the only address sensord ever writes
	ecData  = 0xa46
)

// Port is byte access to the I/O port space (/dev/port).
type Port interface {
	WriteByteAt(addr int64, b byte) error
	ReadByteAt(addr int64) (byte, error)
}

func ecRead(p Port, reg byte) (byte, error) {
	if err := p.WriteByteAt(ecIndex, reg); err != nil {
		return 0, fmt.Errorf("ec index 0x%x: %w", reg, err)
	}
	b, err := p.ReadByteAt(ecData)
	if err != nil {
		return 0, fmt.Errorf("ec register 0x%x: %w", reg, err)
	}
	return b, nil
}

// BoardTemps reads the IT8784's three temperature registers, in degrees Celsius.
func BoardTemps(p Port) ([3]float64, error) {
	var t [3]float64
	for i, reg := range []byte{0x29, 0x2A, 0x2B} {
		b, err := ecRead(p, reg)
		if err != nil {
			return t, err
		}
		t[i] = float64(b)
	}
	return t, nil
}

// FanOutput is one fan output's mode: automatic, or manual at ManualPWM (0..1).
type FanOutput struct {
	Output    int
	Automatic bool
	ManualPWM float64
}

// FanOutputs reads the three outputs' modes (bit 7 set = automatic) and manual PWM duty.
func FanOutputs(p Port) ([3]FanOutput, error) {
	var out [3]FanOutput
	modes := []byte{0x15, 0x16, 0x17}
	pwms := []byte{0x63, 0x6B, 0x73}
	for i := range out {
		mode, err := ecRead(p, modes[i])
		if err != nil {
			return out, err
		}
		pwm, err := ecRead(p, pwms[i])
		if err != nil {
			return out, err
		}
		out[i] = FanOutput{Output: i + 1, Automatic: mode&0x80 != 0, ManualPWM: float64(pwm) / 255}
	}
	return out, nil
}
