package calls

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

const (
	recRate   = 8000
	recHeader = 44
	// boxCapByte is 10 s of mono S16. The player runs up to ~4.6 s ahead of the sound card (the 64 KiB
	// pipe to aplay plus ALSA's buffer), so a long sentence arrives seconds before the clock reaches it;
	// the cap is only a guard for when Them stops (the card went away), never a limit a sentence meets.
	boxCapByte = 10 * recRate * 2
)

var errRecorderClosed = errors.New("calls: recorder is closed")

// Recorder writes a call as a two-channel 8 kHz S16 WAV: left is the other party, right is the box.
// The other party's frames are the clock: the sound card delivers them every 20 ms whether anyone is
// talking or not, while the box's voice arrives in bursts, so each input sample takes the next box
// sample (or silence) and the two channels stay aligned in time without a second timer.
type Recorder struct {
	mu     sync.Mutex
	w      io.WriteSeeker
	box    []byte // pending box audio, always starting on a sample boundary
	half   []byte // a left-channel byte whose sample the sound card split across reads
	data   int64
	closed bool
}

// NewRecorder writes a placeholder header; Close fixes the sizes once they are known.
func NewRecorder(w io.WriteSeeker) (*Recorder, error) {
	r := &Recorder{w: w}
	if _, err := w.Write(wavHeader(0)); err != nil {
		return nil, err
	}
	return r, nil
}

func wavHeader(data uint32) []byte {
	h := make([]byte, recHeader)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+data)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], 2)
	binary.LittleEndian.PutUint32(h[24:], recRate)
	binary.LittleEndian.PutUint32(h[28:], recRate*4)
	binary.LittleEndian.PutUint16(h[32:], 4)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], data)
	return h
}

// Them records one input frame as the left channel and the same span of the box buffer as the
// right. A trailing odd byte waits for the next frame. After Close it returns an error.
func (r *Recorder) Them(frame []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errRecorderClosed
	}
	in := append(r.half, frame...)
	n := len(in) / 2
	r.half = append([]byte(nil), in[2*n:]...)
	if n == 0 {
		return nil
	}
	out := make([]byte, 4*n)
	for i := 0; i < n; i++ {
		out[4*i], out[4*i+1] = in[2*i], in[2*i+1]
		// the right channel stays zero when the box has nothing to say
	}
	take := 2 * n
	if take > len(r.box)&^1 {
		take = len(r.box) &^ 1
	}
	for i := 0; i < take/2; i++ {
		out[4*i+2], out[4*i+3] = r.box[2*i], r.box[2*i+1]
	}
	r.box = r.box[take:]
	w, err := r.w.Write(out)
	r.data += int64(w)
	return err
}

// Box queues the box's own voice for the right channel. It never fails: past ten seconds ahead of
// the clock the oldest audio is dropped, and after Close it is ignored.
func (r *Recorder) Box(pcm []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.box = append(r.box, pcm...)
	if over := len(r.box) - boxCapByte; over > 0 {
		r.box = r.box[(over+1)&^1:] // whole samples only, so the rest stays aligned
	}
}

// Close fixes the RIFF and data sizes. Later calls return nil; Them after it errors.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if _, err := r.w.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := r.w.Write(wavHeader(uint32(r.data)))
	return err
}
