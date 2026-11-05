package calls

import "encoding/binary"

// G.711 μ-law: the phone line's own 8-bit encoding, which the voice model takes and returns. The
// EC25's sound card speaks 16-bit linear (S16_LE), so the call's audio is converted both ways.

const (
	ulawBias = 0x84
	ulawClip = 32635
)

// toULaw encodes S16_LE mono samples, one byte each; a trailing half sample is dropped.
func toULaw(pcm []byte) []byte {
	out := make([]byte, len(pcm)/2)
	for i := range out {
		out[i] = encodeULaw(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
	}
	return out
}

// fromULaw decodes μ-law bytes to S16_LE mono samples.
func fromULaw(ulaw []byte) []byte {
	out := make([]byte, 2*len(ulaw))
	for i, u := range ulaw {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(decodeULaw(u)))
	}
	return out
}

func encodeULaw(s int16) byte {
	x, sign := int(s), byte(0)
	if x < 0 {
		x, sign = -x, 0x80
	}
	x = min(x, ulawClip) + ulawBias
	exp := 7
	for mask := 0x4000; x&mask == 0 && exp > 0; mask >>= 1 {
		exp--
	}
	mantissa := byte(x>>(exp+3)) & 0x0F
	return ^(sign | byte(exp)<<4 | mantissa)
}

func decodeULaw(u byte) int16 {
	u = ^u
	exp, mantissa := int(u>>4)&0x07, int(u&0x0F)
	x := ((mantissa<<3)+ulawBias)<<exp - ulawBias
	if u&0x80 != 0 {
		return int16(-x)
	}
	return int16(x)
}
