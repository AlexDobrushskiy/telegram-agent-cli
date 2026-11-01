package e2e

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
)

// ErrNotOpus means a file is not an Ogg/Opus stream (what a voice note must be).
var ErrNotOpus = errors.New("not an ogg/opus file")

// OpusDuration returns the length in whole seconds (at least 1) of an Ogg/Opus
// stream: the last page's granule position minus the pre-skip, at 48 kHz.
func OpusDuration(b []byte) (int, error) {
	if !bytes.HasPrefix(b, []byte("OggS")) {
		return 0, ErrNotOpus
	}
	head := bytes.Index(b, []byte("OpusHead"))
	if head < 0 || head+12 > len(b) {
		return 0, ErrNotOpus
	}
	preSkip := int64(binary.LittleEndian.Uint16(b[head+10:]))
	last := bytes.LastIndex(b, []byte("OggS"))
	if last+14 > len(b) {
		return 0, ErrNotOpus
	}
	granule := int64(binary.LittleEndian.Uint64(b[last+6:]))
	samples := granule - preSkip
	if granule < 0 || samples <= 0 {
		return 1, nil
	}
	return max(1, int(math.Round(float64(samples)/48000))), nil
}
