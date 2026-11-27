package meltysynth

import (
	"math"
	"testing"
	"time"
)

// newTestSoundFont builds a minimal but fully playable SoundFont entirely in
// memory. It contains a single preset/instrument region backed by a constant
// waveform, so the start of a note is detectable as the first non-silent PCM
// sample without needing an external .sf2 file.
func newTestSoundFont(sampleRate int32) *SoundFont {
	// Long enough for every test to stay inside the (non-looping) waveform.
	waveLength := sampleRate * 30
	wave := make([]int16, waveLength)
	for i := range wave {
		wave[i] = 10000
	}

	header := &SampleHeader{
		Name:          "test",
		Start:         0,
		End:           waveLength,
		StartLoop:     0,
		EndLoop:       waveLength,
		SampleRate:    sampleRate,
		OriginalPitch: 60,
		SampleType:    1,
	}

	instrumentRegion := &InstrumentRegion{Sample: header}
	instrumentRegion.gs[gen_InitialFilterCutoffFrequency] = 13500
	instrumentRegion.gs[gen_DelayVolumeEnvelope] = -32768
	instrumentRegion.gs[gen_AttackVolumeEnvelope] = -32768
	instrumentRegion.gs[gen_HoldVolumeEnvelope] = -32768
	instrumentRegion.gs[gen_DecayVolumeEnvelope] = -32768
	instrumentRegion.gs[gen_ReleaseVolumeEnvelope] = -32768
	instrumentRegion.gs[gen_KeyRange] = 0x7F00
	instrumentRegion.gs[gen_VelocityRange] = 0x7F00
	instrumentRegion.gs[gen_ScaleTuning] = 100
	instrumentRegion.gs[gen_SampleID] = 0
	instrumentRegion.gs[gen_SampleModes] = int16(loop_NoLoop)

	instrument := &Instrument{
		Name:    "test-instrument",
		Regions: []*InstrumentRegion{instrumentRegion},
	}

	presetRegion := &PresetRegion{Instrument: instrument}
	presetRegion.gs[gen_KeyRange] = 0x7F00
	presetRegion.gs[gen_VelocityRange] = 0x7F00

	preset := &Preset{
		Name:        "test-preset",
		PatchNumber: 0,
		BankNumber:  0,
		Regions:     []*PresetRegion{presetRegion},
	}

	return &SoundFont{
		BitsPerSample: 16,
		WaveData:      wave,
		SampleHeaders: []*SampleHeader{header},
		Presets:       []*Preset{preset},
		Instruments:   []*Instrument{instrument},
	}
}

func newTestSynthesizer(t *testing.T, blockSize int32) *Synthesizer {
	t.Helper()
	const sampleRate int32 = 44100
	settings := NewSynthesizerSettings(sampleRate)
	settings.BlockSize = blockSize
	settings.EnableReverbAndChorus = false
	synth, err := NewSynthesizer(newTestSoundFont(sampleRate), settings)
	if err != nil {
		t.Fatalf("failed to create synthesizer: %v", err)
	}
	return synth
}

func newTestMidiFile(messages []message, times []time.Duration) *MidiFile {
	return &MidiFile{messages: messages, times: times}
}

func noteOn(channel, key, velocity byte) message {
	return common3b(0x90|channel, key, velocity)
}

func noteOff(channel, key byte) message {
	return common3b(0x80|channel, key, 0)
}

// oneTick120BPM is the duration of one tick at 120 BPM with a resolution of
// 480 ticks per beat: 60 / (120 * 480) seconds.
var oneTick120BPM = time.Second * 60 / (120 * 480)

// renderChunks renders the whole song using Render calls of the given chunk
// size and returns the concatenated left/right buffers.
func renderChunks(seq *MidiFileSequencer, total int, chunkSize int) ([]float32, []float32) {
	left := make([]float32, total)
	right := make([]float32, total)
	for start := 0; start < total; start += chunkSize {
		end := start + chunkSize
		if end > total {
			end = total
		}
		seq.Render(left[start:end], right[start:end])
	}
	return left, right
}

func firstAudibleSample(buf []float32) int {
	for i, v := range buf {
		if math.Abs(float64(v)) > 1.0e-6 {
			return i
		}
	}
	return -1
}

// Test that 1 tick at 44.1 kHz / 120 BPM / 480 ticks per beat maps to about
// 46 samples, and the note actually starts at that sample instead of being
// pushed to the next 64-sample synthesizer block boundary.
func TestSequencerNoteStartsAtExactSample(t *testing.T) {
	const sampleRate int32 = 44100

	// One tick at 120 BPM with a resolution of 480 ticks per beat.
	oneTick := oneTick120BPM
	expectedSample := int64(math.Round(float64(oneTick) * float64(sampleRate) / float64(time.Second)))
	if expectedSample != 46 {
		t.Fatalf("expected 1 tick to be 46 samples, got %d", expectedSample)
	}

	midi := newTestMidiFile(
		[]message{noteOn(0, 60, 100)},
		[]time.Duration{oneTick},
	)

	for _, blockSize := range []int32{8, 64, 256, 1024} {
		synth := newTestSynthesizer(t, blockSize)
		seq := NewMidiFileSequencer(synth)
		seq.Play(midi, false)

		left, _ := renderChunks(seq, 256, 37)
		got := firstAudibleSample(left)
		if got != int(expectedSample) {
			t.Errorf("block size %d: note started at sample %d, want %d", blockSize, got, expectedSample)
		}
	}
}

// Rendering the whole song at once must produce exactly the same PCM output as
// rendering it in arbitrarily sized chunks, on both channels.
func TestSequencerChunkingProducesIdenticalOutput(t *testing.T) {
	oneTick := oneTick120BPM
	messages := []message{
		noteOn(0, 60, 100),
		noteOn(1, 64, 100),
		noteOff(0, 60),
		noteOff(1, 64),
		noteOn(2, 72, 100),
	}
	times := []time.Duration{
		oneTick,
		3 * oneTick,
		10 * oneTick,
		17 * oneTick,
		23 * oneTick,
	}
	midi := newTestMidiFile(messages, times)

	const total = 44100 / 10 // 100 ms

	// For every supported block size, changing the Render chunk length must
	// not change the output. Different block sizes themselves legitimately
	// change the envelope smoothing granularity, so each block size has its
	// own one-shot reference.
	for _, blockSize := range []int32{8, 64, 256, 1024} {
		referenceSynth := newTestSynthesizer(t, blockSize)
		referenceSeq := NewMidiFileSequencer(referenceSynth)
		referenceSeq.Play(midi, false)
		refLeft, refRight := renderChunks(referenceSeq, total, total)

		chunkSizes := []int{1, 2, 3, 7, 13, 31, 63, 64, 65, 100, 127, 1000, 4096}
		for _, chunkSize := range chunkSizes {
			synth := newTestSynthesizer(t, blockSize)
			seq := NewMidiFileSequencer(synth)
			seq.Play(midi, false)
			left, right := renderChunks(seq, total, chunkSize)

			for i := 0; i < total; i++ {
				if left[i] != refLeft[i] {
					t.Fatalf("block %d chunk %d: left sample %d = %v, want %v", blockSize, chunkSize, i, left[i], refLeft[i])
				}
				if right[i] != refRight[i] {
					t.Fatalf("block %d chunk %d: right sample %d = %v, want %v", blockSize, chunkSize, i, right[i], refRight[i])
				}
			}
		}
	}
}

// Events at the same sample position must be dispatched in their stored order,
// including note-on and note-off events from multiple tracks merged together.
func TestSequencerSameSampleEventsKeepOrder(t *testing.T) {
	oneTick := oneTick120BPM
	// Two different tracks whose events land on the same tick are merged by
	// mergeTracks; emulate the resulting ordering directly.
	noteOnA := noteOn(0, 60, 100)
	noteOnB := noteOn(1, 62, 100)
	noteOffA := noteOff(0, 60)
	midi := newTestMidiFile(
		[]message{noteOnA, noteOnB, noteOffA},
		[]time.Duration{5 * oneTick, 5 * oneTick, 9 * oneTick},
	)

	synth := newTestSynthesizer(t, 64)
	seq := NewMidiFileSequencer(synth)
	seq.Play(midi, false)

	// Rendering across many small chunks must not duplicate or drop events.
	left, _ := renderChunks(seq, 4410, 5)
	if firstAudibleSample(left) != int(durationToSample(5*oneTick, 44100)) {
		t.Fatalf("simultaneous events were not dispatched at the expected sample")
	}
	// The note-off only affects channel 0; the voice on channel 1 stays
	// playing, while the one on channel 0 must have left the playing state.
	var playing int
	for i := 0; i < len(synth.voices.voices); i++ {
		if i < int(synth.voices.activeVoiceCount) && synth.voices.voices[i].voiceState == voice_Playing {
			playing++
		}
	}
	if playing != 1 {
		t.Fatalf("expected exactly one still-playing voice after the note-off, got %d", playing)
	}
}

// A note-off placed at the last event of the song must still be processed,
// even when it lies close to the end of the rendered range.
func TestSequencerNoteOffNearEndIsProcessed(t *testing.T) {
	oneTick := oneTick120BPM
	midi := newTestMidiFile(
		[]message{
			noteOn(0, 60, 100),
			noteOff(0, 60),
		},
		[]time.Duration{oneTick, 4 * oneTick},
	)

	synth := newTestSynthesizer(t, 64)
	seq := NewMidiFileSequencer(synth)
	seq.Play(midi, false)

	total := int(durationToSample(4*oneTick, 44100)) + 1
	left, _ := renderChunks(seq, total, 3)
	if firstAudibleSample(left) != int(durationToSample(oneTick, 44100)) {
		t.Fatalf("note did not start at the expected sample")
	}
	for i := int32(0); i < synth.voices.activeVoiceCount; i++ {
		voice := synth.voices.voices[i]
		if voice.voiceState == voice_Playing {
			t.Fatalf("the note-off at the end of the song was not processed")
		}
	}
}

// Looping must restart exactly at the loop boundary, re-dispatch the opening
// events once, and never replay events twice or lose them across the boundary,
// regardless of how the Render calls are chunked.
func TestSequencerLoopBoundary(t *testing.T) {
	oneTick := oneTick120BPM
	// The last event is a no-op controller message far after the note ended,
	// so there is a guaranteed silent region around the loop boundary.
	midi := newTestMidiFile(
		[]message{
			noteOn(0, 60, 100),
			noteOff(0, 60),
			common3b(0xB0, 0x07, 100), // channel volume, unchanged value
		},
		[]time.Duration{oneTick, 4 * oneTick, 40 * oneTick},
	)

	loopLength := durationToSample(40*oneTick, 44100)
	firstAttack := int(durationToSample(oneTick, 44100))
	total := int(loopLength)*2 + 10

	// Reference: render the whole range in a single call.
	referenceSynth := newTestSynthesizer(t, 64)
	referenceSeq := NewMidiFileSequencer(referenceSynth)
	referenceSeq.Play(midi, true)
	refLeft, refRight := renderChunks(referenceSeq, total, total)

	for _, chunkSize := range []int{1, 5, 17, 64, 1000} {
		synth := newTestSynthesizer(t, 64)
		seq := NewMidiFileSequencer(synth)
		seq.Play(midi, true)
		left, right := renderChunks(seq, total, chunkSize)

		for i := 0; i < total; i++ {
			if left[i] != refLeft[i] || right[i] != refRight[i] {
				t.Fatalf("chunk %d: sample %d differs from the one-shot render (%v/%v vs %v/%v)",
					chunkSize, i, left[i], right[i], refLeft[i], refRight[i])
			}
		}

		// First iteration attack.
		if first := firstAudibleSample(left); first != firstAttack {
			t.Fatalf("chunk %d: first attack at %d, want %d", chunkSize, first, firstAttack)
		}

		// The silent region around the boundary must stay silent.
		for i := int(loopLength) - 20; i < int(loopLength); i++ {
			if math.Abs(float64(left[i])) > 1.0e-6 {
				t.Fatalf("chunk %d: unexpected output at %d before the loop boundary", chunkSize, i)
			}
		}

		// The note of the second iteration attacks at the same offset again.
		secondAttack := int(loopLength) + firstAttack
		if math.Abs(float64(left[secondAttack])) <= 1.0e-6 {
			t.Fatalf("chunk %d: second attack missing at %d", chunkSize, secondAttack)
		}
	}
}

// Tempo changes inside the song are already baked into the event timestamps by
// mergeTracks, so events after a tempo change must still land on their exact
// sample positions when rendered across chunk boundaries.
func TestSequencerTempoChangeTiming(t *testing.T) {
	// Build a two-track file with a tempo change through the real parser path.
	// 120 BPM initially, then 240 BPM (one beat becomes twice as short).
	resolution := int16(480)
	track0 := []message{
		tempoChange(500000), // 120 BPM
		tempoChange(250000), // 240 BPM
		endOfTrack(),
	}
	track0Ticks := []int32{0, 480, 480}
	track1 := []message{
		noteOn(0, 60, 100),
		noteOff(0, 60),
		endOfTrack(),
	}
	track1Ticks := []int32{1, 720, 720}

	messages, times := mergeTracks(
		[][]message{track0, track1},
		[][]int32{track0Ticks, track1Ticks},
		resolution,
	)
	midi := newTestMidiFile(messages, times)

	// Tick 1 (before any tempo change) maps to ~46 samples. The tempo change
	// occurs at tick 480 (0.5 s). From there, 240 ticks at 240 BPM take
	// 0.125 s, so the note-off lands at 0.625 s: a non-aligned position that
	// exercises both tempo segments and the rounding of the conversion.
	wantNoteOn := int64(46)
	// 0.625 s * 44100 = 27562.5; the float time map carries a tiny negative
	// rounding error, so the nearest sample is 27562.
	wantNoteOff := int64(27562)

	synth := newTestSynthesizer(t, 64)
	seq := NewMidiFileSequencer(synth)
	seq.Play(midi, false)
	left, _ := renderChunks(seq, 44100, 29)
	if got := firstAudibleSample(left); got != int(wantNoteOn) {
		t.Fatalf("note-on at %d, want %d", got, wantNoteOn)
	}

	// Locate the note-off by command (end-of-track markers are kept in the
	// merged event list, so a fixed index would be fragile).
	var noteOffSample int64 = -1
	for i, msg := range messages {
		if msg.command == 0x80 {
			noteOffSample = seq.eventSamples[i]
		}
	}
	if noteOffSample != wantNoteOff {
		t.Fatalf("note-off scheduled at %d, want %d", noteOffSample, wantNoteOff)
	}

	// The tempo change must not make chunked rendering diverge from the
	// one-shot render on either channel.
	refSynth := newTestSynthesizer(t, 64)
	refSeq := NewMidiFileSequencer(refSynth)
	refSeq.Play(midi, false)
	refLeft, refRight := renderChunks(refSeq, 44100, 44100)
	for _, chunkSize := range []int{1, 17, 63, 64, 65, 1000} {
		s := newTestSynthesizer(t, 64)
		q := NewMidiFileSequencer(s)
		q.Play(midi, false)
		gotLeft, gotRight := renderChunks(q, 44100, chunkSize)
		for i := range gotLeft {
			if gotLeft[i] != refLeft[i] || gotRight[i] != refRight[i] {
				t.Fatalf("chunk %d: sample %d differs across chunking (%v/%v vs %v/%v)",
					chunkSize, i, gotLeft[i], gotRight[i], refLeft[i], refRight[i])
			}
		}
	}
}

// Very large timestamps must convert without overflowing the integer result.
func TestDurationToSampleOverflow(t *testing.T) {
	// Negative durations are clamped to zero.
	if got := durationToSample(time.Duration(math.MinInt64), 44100); got != 0 {
		t.Fatalf("negative duration should clamp to 0, got %d", got)
	}

	// The largest representable duration (~292 years) must still produce a
	// valid, positive sample count rather than wrapping to a negative number.
	huge := durationToSample(time.Duration(math.MaxInt64), 44100)
	if huge <= 0 {
		t.Fatalf("huge duration should map to a positive sample count, got %d", huge)
	}
	expectedHuge := int64(math.Round(float64(time.Duration(math.MaxInt64)) * 44100.0 / float64(time.Second)))
	if huge != expectedHuge {
		t.Fatalf("huge duration = %d samples, want %d", huge, expectedHuge)
	}

	// A normal, realistic value keeps its rounded precision.
	oneTick := oneTick120BPM
	if got := durationToSample(oneTick, 44100); got != 46 {
		t.Fatalf("1 tick = %d samples, want 46", got)
	}
}

// Stop must silence the synthesizer and Render calls afterwards stay silent.
func TestSequencerStop(t *testing.T) {
	oneTick := oneTick120BPM
	midi := newTestMidiFile(
		[]message{noteOn(0, 60, 100)},
		[]time.Duration{oneTick},
	)

	synth := newTestSynthesizer(t, 64)
	seq := NewMidiFileSequencer(synth)
	seq.Play(midi, false)
	left, _ := renderChunks(seq, 512, 64)
	if firstAudibleSample(left) < 0 {
		t.Fatal("expected audible output before Stop")
	}

	seq.Stop()
	silentLeft := make([]float32, 512)
	silentRight := make([]float32, 512)
	seq.Render(silentLeft, silentRight)
	for i := range silentLeft {
		if silentLeft[i] != 0 || silentRight[i] != 0 {
			t.Fatalf("output after Stop must be silent, sample %d = (%v, %v)", i, silentLeft[i], silentRight[i])
		}
	}
}
