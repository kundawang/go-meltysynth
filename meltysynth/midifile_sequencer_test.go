package meltysynth

import (
	"math"
	"testing"
	"time"
)

// buildLongNoteMidi builds the cross-segment long-note sample: a single
// sustained note that spans the whole tempo curve, with controller events
// in between. The embedded tempo is a constant 120 BPM, which the curve
// overrides during curve playback.
func buildLongNoteMidi(t *testing.T) *MidiFile {
	t.Helper()
	return buildMidi(t, 480, []midiEvent{
		evTimeSignature(0, 4, 2),
		evTempo(0, 120),
		evControlChange(0, 0, 7, 127),
		evNoteOn(0, 0, 60, 100),
		evControlChange(480, 0, 7, 100),
		evControlChange(960, 0, 7, 90),
		evControlChange(1440, 0, 7, 80),
		evNoteOff(3840, 0, 60),
		evEndOfTrack(3840),
	})
}

// buildAccelCurve builds a curve that accelerates from 60 BPM to 240 BPM
// over the first 1920 ticks (one 4/4 bar), then stays at 240 BPM.
func buildAccelCurve(t *testing.T) *TempoCurve {
	t.Helper()
	curve, err := NewTempoCurve(480, []TempoPoint{
		{Tick: 0, Bpm: 60},
		{Tick: 1920, Bpm: 240},
	})
	if err != nil {
		t.Fatal(err)
	}
	return curve
}

// accelCurveEndSeconds is the analytic playback time of tick 3840 under
// buildAccelCurve: (60/480) * (1920/180) * ln(4) + 1920 * 60 / (480*240).
func accelCurveEndSeconds() float64 {
	return (60.0/480)*(1920.0/180)*math.Log(4) + 1.0
}

func renderWithSequencer(seq *MidiFileSequencer, sampleRate int32, length int, chunkSize int) ([]float32, []float32) {
	left := make([]float32, length)
	right := make([]float32, length)
	if chunkSize <= 0 {
		seq.Render(left, right)
		return left, right
	}
	for offset := 0; offset < length; offset += chunkSize {
		end := offset + chunkSize
		if end > length {
			end = length
		}
		seq.Render(left[offset:end], right[offset:end])
	}
	return left, right
}

func newSequencerPair(t *testing.T, soundFont *SoundFont, sampleRate int32) (*Synthesizer, *MidiFileSequencer) {
	t.Helper()
	settings := NewSynthesizerSettings(sampleRate)
	synthesizer, err := NewSynthesizer(soundFont, settings)
	if err != nil {
		t.Fatal(err)
	}
	return synthesizer, NewMidiFileSequencer(synthesizer)
}

func TestSequencerTempoCurveEventTimes(t *testing.T) {
	soundFont := loadGM(t)
	midiFile := buildLongNoteMidi(t)
	curve := buildAccelCurve(t)

	_, sequencer := newSequencerPair(t, soundFont, 44100)
	sequencer.PlayWithTempoCurve(midiFile, curve, false)

	// The merged messages keep their order: CC, note on, CC x3, note off, EOT.
	expectedCommands := []byte{0xB0, 0x90, 0xB0, 0xB0, 0xB0, 0x80, 0xFF}
	if len(midiFile.messages) != len(expectedCommands) {
		t.Fatalf("expected %d messages, but got %d", len(expectedCommands), len(midiFile.messages))
	}
	for i, command := range expectedCommands {
		msg := midiFile.messages[i]
		var actual byte
		if msg.getMessageType() == msg_EndOfTrack {
			actual = 0xFF
		} else {
			actual = msg.command
		}
		if actual != command {
			t.Fatalf("messages[%d]: expected command %#x, but got %#x", i, command, actual)
		}
	}

	// Each event must be scheduled at the curve-mapped time of its tick, and
	// the times must be non-decreasing so that the order is preserved.
	previous := time.Duration(0)
	for i := range midiFile.messages {
		expected := curve.TickToTime(midiFile.ticks[i])
		if sequencer.times[i] != expected {
			t.Fatalf("times[%d]: expected %v, but got %v", i, expected, sequencer.times[i])
		}
		if sequencer.times[i] < previous {
			t.Fatalf("times[%d] went backwards: %v < %v", i, sequencer.times[i], previous)
		}
		previous = sequencer.times[i]
	}

	// Check the event sample points against analytic values.
	// The note-on is at tick 0 (sample 0); the note-off is at tick 3840.
	sampleRate := float64(44100)
	noteOffSample := curve.TickToTime(3840).Seconds() * sampleRate
	areEqual(t, noteOffSample, accelCurveEndSeconds()*sampleRate)

	// The total playback length must follow the curve, not the embedded tempo.
	areEqual(t, midiFile.GetLengthWithTempoCurve(curve).Seconds(), accelCurveEndSeconds())
}

func TestSequencerTempoCurveChunkedEqualsOneShot(t *testing.T) {
	soundFont := loadGM(t)
	midiFile := buildLongNoteMidi(t)
	curve := buildAccelCurve(t)

	sampleRate := int32(44100)
	length := int(math.Ceil(midiFile.GetLengthWithTempoCurve(curve).Seconds()*float64(sampleRate))) + int(sampleRate)

	// One-shot rendering.
	synth, seq := newSequencerPair(t, soundFont, sampleRate)
	seq.PlayWithTempoCurve(midiFile, curve, false)
	wholeLeft, wholeRight := renderWithSequencer(seq, sampleRate, length, 0)

	// Chunked rendering with various chunk sizes, including sizes that do
	// not align with the synthesizer block size.
	for _, chunkSize := range []int{1, 997, 4096} {
		synth, seq = newSequencerPair(t, soundFont, sampleRate)
		seq.PlayWithTempoCurve(midiFile, curve, false)
		chunkedLeft, chunkedRight := renderWithSequencer(seq, sampleRate, length, chunkSize)

		for i := 0; i < length; i++ {
			if chunkedLeft[i] != wholeLeft[i] || chunkedRight[i] != wholeRight[i] {
				t.Fatalf("chunk size %d: sample %d differs: (%v, %v) vs (%v, %v)",
					chunkSize, i, chunkedLeft[i], chunkedRight[i], wholeLeft[i], wholeRight[i])
			}
		}
	}
	_ = synth
}

func TestSequencerTempoCurveLongNoteNotRetriggered(t *testing.T) {
	soundFont := loadGM(t)
	midiFile := buildLongNoteMidi(t)
	curve := buildAccelCurve(t)

	sampleRate := int32(44100)
	synthesizer, sequencer := newSequencerPair(t, soundFont, sampleRate)
	sequencer.PlayWithTempoCurve(midiFile, curve, false)

	// Render block by block, watching the active voice count. The long note
	// spans the curve boundary at tick 1920; if it were retriggered there,
	// a second voice would appear.
	length := int(math.Ceil(midiFile.GetLengthWithTempoCurve(curve).Seconds()*float64(sampleRate))) + 5*int(sampleRate)
	blockSize := int(synthesizer.BlockSize)
	left := make([]float32, blockSize)
	right := make([]float32, blockSize)

	started := false
	maxVoices := int32(0)
	nonSilent := false
	for rendered := 0; rendered < length; rendered += blockSize {
		sequencer.Render(left, right)
		voices := synthesizer.voices.activeVoiceCount
		if voices > maxVoices {
			maxVoices = voices
		}
		if voices > 0 {
			started = true
		}
		for i := range left {
			if left[i] != 0 || right[i] != 0 {
				nonSilent = true
				break
			}
		}
	}

	if !started {
		t.Fatal("the note was never started")
	}
	if maxVoices != 1 {
		t.Fatalf("expected at most 1 active voice (no retrigger), but got %d", maxVoices)
	}
	if !nonSilent {
		t.Fatal("the rendered output is silent")
	}
}

func TestSequencerWithoutCurveUnchanged(t *testing.T) {
	soundFont := loadGM(t)
	midiFile := buildLongNoteMidi(t)

	sampleRate := int32(44100)
	length := int(math.Ceil(midiFile.GetLength().Seconds()*float64(sampleRate))) + int(sampleRate)

	// Play without a curve: the event times must be the MIDI file's own times.
	_, seq := newSequencerPair(t, soundFont, sampleRate)
	seq.Play(midiFile, false)
	for i := range midiFile.messages {
		if seq.times[i] != midiFile.times[i] {
			t.Fatalf("times[%d]: expected %v, but got %v", i, midiFile.times[i], seq.times[i])
		}
	}
	wholeLeft, wholeRight := renderWithSequencer(seq, sampleRate, length, 0)

	// Chunked rendering must match the one-shot rendering here as well.
	_, seq = newSequencerPair(t, soundFont, sampleRate)
	seq.Play(midiFile, false)
	chunkedLeft, chunkedRight := renderWithSequencer(seq, sampleRate, length, 997)
	for i := 0; i < length; i++ {
		if chunkedLeft[i] != wholeLeft[i] || chunkedRight[i] != wholeRight[i] {
			t.Fatalf("sample %d differs between chunked and one-shot rendering", i)
		}
	}

	// The curve playback must actually differ from the fixed-tempo playback.
	curve := buildAccelCurve(t)
	_, seq = newSequencerPair(t, soundFont, sampleRate)
	seq.PlayWithTempoCurve(midiFile, curve, false)
	curveLeft, _ := renderWithSequencer(seq, sampleRate, length, 0)
	different := false
	for i := 0; i < length; i++ {
		if curveLeft[i] != wholeLeft[i] {
			different = true
			break
		}
	}
	if !different {
		t.Fatal("the tempo curve had no effect on the rendered output")
	}
}
