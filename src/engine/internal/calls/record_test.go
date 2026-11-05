package calls

import (
	"encoding/binary"
	"os"
	"testing"
)

// recorderFile is a real file in a short temp dir: the recorder seeks back to fix the header.
func recorderFile(t *testing.T) (*os.File, *Recorder) {
	t.Helper()
	dir, err := os.MkdirTemp("", "rec")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f, err := os.Create(dir + "/a.wav")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	r, err := NewRecorder(f)
	if err != nil {
		t.Fatal(err)
	}
	return f, r
}

func slurp(t *testing.T, f *os.File) []byte {
	t.Helper()
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ramp is n samples whose values start at first, little-endian.
func ramp(first, n int) []byte {
	b := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(first+i))
	}
	return b
}

func TestTheHeaderDescribesStereo8kHz16Bit(t *testing.T) {
	f, r := recorderFile(t)
	if err := r.Them(ramp(1, 160)); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	got := slurp(t, f)
	data := 160 * 4
	want := make([]byte, 44)
	copy(want[0:], "RIFF")
	binary.LittleEndian.PutUint32(want[4:], uint32(36+data))
	copy(want[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(want[16:], 16)
	binary.LittleEndian.PutUint16(want[20:], 1)
	binary.LittleEndian.PutUint16(want[22:], 2)
	binary.LittleEndian.PutUint32(want[24:], 8000)
	binary.LittleEndian.PutUint32(want[28:], 32000)
	binary.LittleEndian.PutUint16(want[32:], 4)
	binary.LittleEndian.PutUint16(want[34:], 16)
	copy(want[36:], "data")
	binary.LittleEndian.PutUint32(want[40:], uint32(data))
	if string(got[:44]) != string(want) {
		t.Fatalf("header\n got %x\nwant %x", got[:44], want)
	}
	if len(got) != 44+data {
		t.Fatalf("file is %d bytes, want %d", len(got), 44+data)
	}
}

// channels splits the file's data into the left and right samples.
func channels(t *testing.T, f *os.File) (l, r []int16) {
	t.Helper()
	d := slurp(t, f)[44:]
	for i := 0; i+4 <= len(d); i += 4 {
		l = append(l, int16(binary.LittleEndian.Uint16(d[i:])))
		r = append(r, int16(binary.LittleEndian.Uint16(d[i+2:])))
	}
	return
}

func TestTheBoxChannelIsPaddedWithSilence(t *testing.T) {
	f, r := recorderFile(t)
	if err := r.Them(ramp(1, 160)); err != nil {
		t.Fatal(err)
	}
	r.Close()
	l, rt := channels(t, f)
	if len(l) != 160 || l[0] != 1 || l[159] != 160 {
		t.Fatalf("left channel wrong: %d samples", len(l))
	}
	for i, v := range rt {
		if v != 0 {
			t.Fatalf("right[%d] = %d, want silence", i, v)
		}
	}
}

func TestBurstsCarryOverToLaterFrames(t *testing.T) {
	f, r := recorderFile(t)
	r.Box(ramp(1000, 320)) // 640 bytes, two frames' worth
	r.Them(ramp(1, 160))
	r.Them(ramp(1, 160))
	r.Them(ramp(1, 160)) // the burst is spent: silence again
	r.Close()
	_, rt := channels(t, f)
	for i := 0; i < 320; i++ {
		if rt[i] != int16(1000+i) {
			t.Fatalf("right[%d] = %d, want %d", i, rt[i], 1000+i)
		}
	}
	for i := 320; i < 480; i++ {
		if rt[i] != 0 {
			t.Fatalf("right[%d] = %d, want silence", i, rt[i])
		}
	}
}

func TestInterleaving(t *testing.T) {
	f, r := recorderFile(t)
	r.Box(ramp(-5, 3))
	r.Them(ramp(10, 3))
	r.Close()
	d := slurp(t, f)[44:]
	want := []int16{10, -5, 11, -4, 12, -3}
	if len(d) != 12 {
		t.Fatalf("%d data bytes, want 12", len(d))
	}
	for i, w := range want {
		if g := int16(binary.LittleEndian.Uint16(d[2*i:])); g != w {
			t.Fatalf("sample %d = %d, want %d", i, g, w)
		}
	}
}

func TestSamplesSplitAcrossReadsStayWhole(t *testing.T) {
	f, r := recorderFile(t)
	in := ramp(300, 4)
	box := ramp(-7, 4)
	r.Box(box[:3]) // one sample and a half
	r.Box(box[3:])
	r.Them(in[:3])
	r.Them(in[3:])
	r.Close()
	l, rt := channels(t, f)
	if len(l) != 4 {
		t.Fatalf("%d samples, want 4", len(l))
	}
	for i := range l {
		if l[i] != int16(300+i) || rt[i] != int16(-7+i) {
			t.Fatalf("sample %d = (%d,%d)", i, l[i], rt[i])
		}
	}
}

func TestTheBoxBufferKeepsTheNewestTenSeconds(t *testing.T) {
	f, r := recorderFile(t)
	r.Box(ramp(0, 84000)) // 4000 samples over the 80000 cap
	r.Them(ramp(1, 1))
	r.Close()
	_, rt := channels(t, f)
	if rt[0] != 4000 {
		t.Fatalf("first right sample = %d, want 4000 (oldest dropped)", rt[0])
	}
}

// The player runs seconds ahead of the sound card (the pipe to aplay and ALSA's buffer), so a long
// sentence arrives well before the clock reaches it: none of it may be lost or shifted.
func TestAFiveSecondBurstIsRecordedWholeAndInOrder(t *testing.T) {
	f, r := recorderFile(t)
	burst := ramp(0, 5*8000)
	for i := 0; i < len(burst); i += 320 { // in 20 ms chunks, all before the clock moves
		r.Box(burst[i : i+320])
	}
	for i := 0; i < 5*50+1; i++ { // 5 s of 20 ms frames, and one more
		r.Them(ramp(160*i, 160))
	}
	r.Close()
	l, rt := channels(t, f)
	if len(rt) != 5*8000+160 {
		t.Fatalf("%d samples, want %d", len(rt), 5*8000+160)
	}
	for i := 0; i < 5*8000; i++ {
		if rt[i] != int16(i) || l[i] != int16(i) {
			t.Fatalf("sample %d = (%d,%d), want (%d,%d)", i, l[i], rt[i], int16(i), int16(i))
		}
	}
	for i := 5 * 8000; i < len(rt); i++ {
		if rt[i] != 0 {
			t.Fatalf("right[%d] = %d, want silence after the burst", i, rt[i])
		}
	}
}

func TestAfterCloseNothingIsWritten(t *testing.T) {
	f, r := recorderFile(t)
	r.Close()
	if err := r.Them(ramp(1, 160)); err == nil {
		t.Fatal("Them after Close should fail")
	}
	r.Box(ramp(1, 10))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if n := len(slurp(t, f)); n != 44 {
		t.Fatalf("file is %d bytes, want 44", n)
	}
}
