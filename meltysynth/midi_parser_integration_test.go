package meltysynth

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// appendVarLen appends a MIDI variable-length quantity.
func appendVarLen(buf *bytes.Buffer, v uint32) {
	var tmp [4]byte
	n := 0
	tmp[3] = byte(v & 0x7f)
	v >>= 7
	for v > 0 {
		n++
		tmp[3-n] = byte(v&0x7f) | 0x80
		v >>= 7
	}
	buf.Write(tmp[3-n:])
}

// buildMinimalMIDI creates a format-1 MIDI file (one tempo/meta track and one
// note track) at 120 BPM with a resolution of 480 ticks per beat, exercising
// the real file parser rather than the in-memory test helper.
func buildMinimalMIDI() []byte {
	var out bytes.Buffer
	put32 := func(v uint32) { _ = binary.Write(&out, binary.BigEndian, v) }
	put16 := func(v uint16) { _ = binary.Write(&out, binary.BigEndian, v) }

	out.WriteString("MThd")
	put32(6)
	put16(1) // format 1
	put16(2) // two tracks
	put16(480)

	// Track 0: tempo 120 BPM (500000 us per quarter note), then end of track.
	var trk0 bytes.Buffer
	trk0.Write([]byte{0x00, 0xFF, 0x51, 0x03, 0x07, 0xA1, 0x20})
	trk0.Write([]byte{0x00, 0xFF, 0x2F, 0x00})
	out.WriteString("MTrk")
	put32(uint32(trk0.Len()))
	out.Write(trk0.Bytes())

	// Track 1: note-on at tick 1 (middle C), note-off at tick 480, end.
	var trk1 bytes.Buffer
	appendVarLen(&trk1, 1)
	trk1.Write([]byte{0x90, 60, 100})
	appendVarLen(&trk1, 479)
	trk1.Write([]byte{0x80, 60, 0})
	trk1.Write([]byte{0x00, 0xFF, 0x2F, 0x00})
	out.WriteString("MTrk")
	put32(uint32(trk1.Len()))
	out.Write(trk1.Bytes())

	return out.Bytes()
}

func TestSequencerFromParsedMidiFile(t *testing.T) {
	mf, err := NewMidiFile(bytes.NewReader(buildMinimalMIDI()))
	if err != nil {
		t.Fatalf("failed to parse MIDI: %v", err)
	}
	if len(mf.messages) < 2 {
		t.Fatalf("expected at least note on/off, got %d messages", len(mf.messages))
	}

	// Note-on is at tick 1 -> ~46 samples under 44.1 kHz / 120 BPM / 480.
	const total = 44100
	refSynth := newTestSynthesizer(t, 64)
	refSeq := NewMidiFileSequencer(refSynth)
	refSeq.Play(mf, false)
	refLeft, refRight := renderChunks(refSeq, total, total)
	if got := firstAudibleSample(refLeft); got != 46 {
		t.Fatalf("parsed-MIDI note-on at %d, want 46", got)
	}

	for _, chunkSize := range []int{1, 29, 64, 65, 333, 4096} {
		synth := newTestSynthesizer(t, 64)
		seq := NewMidiFileSequencer(synth)
		seq.Play(mf, false)
		left, right := renderChunks(seq, total, chunkSize)
		for i := range left {
			if left[i] != refLeft[i] || right[i] != refRight[i] {
				t.Fatalf("chunk %d sample %d differs from the one-shot render", chunkSize, i)
			}
		}
		if math.Abs(float64(left[45])) > 1e-9 {
			t.Fatalf("chunk %d: sample 45 should be silent", chunkSize)
		}
		if math.Abs(float64(left[46])) <= 1e-9 {
			t.Fatalf("chunk %d: sample 46 should be audible", chunkSize)
		}
	}
}
