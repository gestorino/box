package calls

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// soundCard is the EC25 as a USB sound card (UAC; infra/bkhd-1264/README.md).
const soundCard = "hw:EC25EUX,0"

// modemAudio is the Audio of the box's modem. Thin: verified on the box.
type modemAudio struct{}

func NewAudio() Audio { return modemAudio{} }

func (modemAudio) Route(ctx context.Context) error   { return at(ctx, atRoute) }
func (modemAudio) Unroute(ctx context.Context) error { return at(ctx, atUnroute) }

// modemDuplex records and plays the routed call through the EC25's sound card, raw S16_LE 8 kHz
// mono, with arecord and aplay. Thin: verified on the box.
type modemDuplex struct{}

func NewDuplex() Duplex { return modemDuplex{} }

func (modemDuplex) Open(ctx context.Context) (io.ReadCloser, io.WriteCloser, error) {
	raw := []string{"-q", "-D", soundCard, "-f", "S16_LE", "-r", "8000", "-c", "1", "-t", "raw"}
	rec := exec.CommandContext(ctx, "arecord", raw...)
	in, err := rec.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	play := exec.Command("aplay", append(raw, "-")...) // not killed with ctx: it plays out what it has
	out, err := play.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := rec.Start(); err != nil {
		return nil, nil, fmt.Errorf("arecord: %w", err)
	}
	if err := play.Start(); err != nil {
		rec.Process.Kill()
		rec.Wait()
		return nil, nil, fmt.Errorf("aplay: %w", err)
	}
	return procReader{in, rec}, procWriter{out, play}, nil
}

// procReader and procWriter end their process with the stream.
type procReader struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (p procReader) Close() error {
	p.cmd.Process.Kill()
	p.ReadCloser.Close()
	p.cmd.Wait()
	return nil
}

type procWriter struct {
	io.WriteCloser
	cmd *exec.Cmd
}

// Close ends the input; aplay plays out what it holds and exits — or is killed after two seconds.
func (p procWriter) Close() error {
	p.WriteCloser.Close()
	exited := make(chan struct{})
	go func() { p.cmd.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		p.cmd.Process.Kill()
		<-exited
	}
	return nil
}
