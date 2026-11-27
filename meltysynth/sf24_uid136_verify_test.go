package meltysynth_test

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	meltysynth "github.com/sinshu/go-meltysynth/meltysynth"
)

// This file builds a tiny but fully loadable SoundFont 2.04 image in memory and
// verifies the optional sm24 / 24 bit playback path. Every helper is prefixed
// with uid136 to avoid clashing with any other in-package test helpers.

func uid136Chunk(id string, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteString(id)
	_ = binary.Write(&b, binary.LittleEndian, int32(len(payload)))
	b.Write(payload)
	if len(payload)%2 == 1 {
		b.WriteByte(0) // RIFF word alignment pad
	}
	return b.Bytes()
}

func uid136RawChunkHeader(id string, size int32) []byte {
	var b bytes.Buffer
	b.WriteString(id)
	_ = binary.Write(&b, binary.LittleEndian, size)
	return b.Bytes()
}

func uid136List(listType string, chunks ...[]byte) []byte {
	var body bytes.Buffer
	body.WriteString(listType)
	for _, c := range chunks {
		body.Write(c)
	}
	var out bytes.Buffer
	out.WriteString("LIST")
	_ = binary.Write(&out, binary.LittleEndian, int32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func uid136Version(major, minor int16) []byte {
	v := make([]byte, 4)
	binary.LittleEndian.PutUint16(v[0:], uint16(major))
	binary.LittleEndian.PutUint16(v[2:], uint16(minor))
	return v
}

// uid136SF2 builds a minimal one-preset/one-instrument/one-sample SoundFont.
// high holds the signed 16 bit smpl words; low, when non-nil, is the sm24
// payload. include24 controls whether an sm24 chunk is emitted at all.
func uid136SF2(major, minor int16, high []int16, low []byte, include24 bool) []byte {
	var riffBody bytes.Buffer

	riffBody.Write(uid136List("INFO",
		uid136Chunk("ifil", uid136Version(major, minor))))

	smplPayload := make([]byte, 2*len(high))
	for i, v := range high {
		binary.LittleEndian.PutUint16(smplPayload[2*i:], uint16(v))
	}
	sdtaChunks := [][]byte{uid136Chunk("smpl", smplPayload)}
	if include24 {
		sdtaChunks = append(sdtaChunks, uid136Chunk("sm24", low))
	}
	riffBody.Write(uid136List("sdta", sdtaChunks...))

	// phdr: one preset + terminator whose bag index points at the pbag end.
	var phdr [38]byte
	copy(phdr[0:20], "Piano")
	phdrTerm := make([]byte, 38)
	binary.LittleEndian.PutUint16(phdrTerm[24:], 1)

	pbag := make([]byte, 8) // one zone + terminating record
	binary.LittleEndian.PutUint16(pbag[4:], 1)
	pgen := make([]byte, 8) // Instrument(41)=0 + terminator
	binary.LittleEndian.PutUint16(pgen[0:], 41)

	var inst [22]byte
	copy(inst[0:20], "Inst")
	instTerm := make([]byte, 22)
	binary.LittleEndian.PutUint16(instTerm[20:], 1)

	ibag := make([]byte, 8)
	binary.LittleEndian.PutUint16(ibag[4:], 1)
	igen := make([]byte, 8) // SampleID(53)=0 + terminator
	binary.LittleEndian.PutUint16(igen[0:], 53)

	n := int32(len(high))
	var sh [46]byte
	binary.LittleEndian.PutUint32(sh[20:], 0)           // start
	binary.LittleEndian.PutUint32(sh[24:], uint32(n-1)) // end (neighbor stays in range)
	binary.LittleEndian.PutUint32(sh[28:], 0)           // start loop
	binary.LittleEndian.PutUint32(sh[32:], uint32(n-1))
	binary.LittleEndian.PutUint32(sh[36:], 44100) // sample rate
	sh[40] = 60                                   // original pitch / root key
	binary.LittleEndian.PutUint16(sh[44:], 1)     // mono sample

	riffBody.Write(uid136List("pdta",
		uid136Chunk("phdr", append(phdr[:], phdrTerm...)),
		uid136Chunk("pbag", pbag),
		uid136Chunk("pgen", pgen),
		uid136Chunk("inst", append(inst[:], instTerm...)),
		uid136Chunk("ibag", ibag),
		uid136Chunk("igen", igen),
		uid136Chunk("shdr", append(sh[:], make([]byte, 46)...))))

	var riff bytes.Buffer
	riff.WriteString("RIFF")
	_ = binary.Write(&riff, binary.LittleEndian, int32(4+riffBody.Len()))
	riff.WriteString("sfbk")
	riff.Write(riffBody.Bytes())
	return riff.Bytes()
}

func uid136Render(t *testing.T, sfBytes []byte, mutate func(*meltysynth.SynthesizerSettings)) []float32 {
	t.Helper()
	sf, err := meltysynth.NewSoundFont(bytes.NewReader(sfBytes))
	if err != nil {
		t.Fatalf("NewSoundFont: %v", err)
	}
	settings := meltysynth.NewSynthesizerSettings(44100)
	settings.EnableReverbAndChorus = false
	if mutate != nil {
		mutate(settings)
	}
	synth, err := meltysynth.NewSynthesizer(sf, settings)
	if err != nil {
		t.Fatalf("NewSynthesizer: %v", err)
	}
	synth.NoteOn(0, 60, 100)
	left := make([]float32, 128)
	right := make([]float32, 128)
	synth.Render(left, right)
	return left
}

func uid136MaxAbsDiff(a, b []float32) float64 {
	var m float64
	for i := range a {
		if d := math.Abs(float64(a[i] - b[i])); d > m {
			m = d
		}
	}
	return m
}

// Requirement 3: low byte 0 vs 128 must differ measurably in 24 bit mode, but
// be bit-identical through the 16 bit fallback.
func TestUID136SF24LowByteDifferential(t *testing.T) {
	const n = 128
	high := make([]int16, n)
	for i := range high {
		high[i] = 8000
	}
	lowZero := make([]byte, n)
	low128 := make([]byte, n)
	for i := range low128 {
		low128[i] = 128
	}

	f0 := uid136SF2(2, 4, high, lowZero, true)
	f128 := uid136SF2(2, 4, high, low128, true)

	sf, err := meltysynth.NewSoundFont(bytes.NewReader(f128))
	if err != nil {
		t.Fatal(err)
	}
	if sf.BitsPerSample != 24 || !sf.Has24BitSamples() {
		t.Fatalf("expected 24 bit combined data, bits=%d has=%v", sf.BitsPerSample, sf.Has24BitSamples())
	}
	if got, want := sf.WaveData24[0], int32(8000)*256+128; got != want {
		t.Fatalf("combined first sample = %d, want %d", got, want)
	}

	out0_24 := uid136Render(t, f0, func(s *meltysynth.SynthesizerSettings) { s.Enable24BitAudio = true })
	out128_24 := uid136Render(t, f128, func(s *meltysynth.SynthesizerSettings) { s.Enable24BitAudio = true })
	if d := uid136MaxAbsDiff(out0_24, out128_24); d < 1e-7 {
		t.Fatalf("24 bit mode should show a measurable low-bit difference, got max diff %g", d)
	}

	// Numeric bit-depth selector must enable the same path.
	out128_24Depth := uid136Render(t, f128, func(s *meltysynth.SynthesizerSettings) { s.BitsPerSample = 24 })
	if d := uid136MaxAbsDiff(out128_24, out128_24Depth); d != 0 {
		t.Fatalf("BitsPerSample=24 should match Enable24BitAudio, diff %g", d)
	}

	// 16 bit fallback (switch off): the low bytes are ignored, so both files
	// render bit-identically.
	out0_16 := uid136Render(t, f0, nil)
	out128_16 := uid136Render(t, f128, nil)
	for i := range out0_16 {
		if math.Float32bits(out0_16[i]) != math.Float32bits(out128_16[i]) {
			t.Fatalf("16 bit fallback differs at sample %d: %v vs %v", i, out0_16[i], out128_16[i])
		}
	}

	// Enabling the switch on a plain 16 bit font (no sm24) must not change
	// the output either.
	f16 := uid136SF2(2, 1, high, nil, false)
	a := uid136Render(t, f16, nil)
	b := uid136Render(t, f16, func(s *meltysynth.SynthesizerSettings) { s.Enable24BitAudio = true })
	if d := uid136MaxAbsDiff(a, b); d != 0 {
		t.Fatalf("16 bit font changed with 24 bit switch on: diff %g", d)
	}
}

// Requirement 2: version / length gates and RIFF alignment.
func TestUID136SF24GatesAndAlignment(t *testing.T) {
	// Odd sample count => odd sm24 payload => one RIFF pad byte.
	const n = 33
	high := make([]int16, n)
	low := make([]byte, n)
	for i := range low {
		low[i] = 128
	}

	if sf, err := meltysynth.NewSoundFont(bytes.NewReader(uid136SF2(2, 4, high, low, true))); err != nil {
		t.Fatalf("odd RIFF alignment should parse: %v", err)
	} else if sf.BitsPerSample != 24 || len(sf.WaveData24) != n {
		t.Fatalf("odd alignment: bits=%d w24=%d", sf.BitsPerSample, len(sf.WaveData24))
	}

	// Missing sm24.
	if sf, err := meltysynth.NewSoundFont(bytes.NewReader(uid136SF2(2, 4, high, nil, false))); err != nil {
		t.Fatal(err)
	} else if sf.BitsPerSample != 16 || sf.Has24BitSamples() {
		t.Fatal("missing sm24 must stay 16 bit")
	}

	// Version gate: 2.1 does not define sm24.
	if sf, err := meltysynth.NewSoundFont(bytes.NewReader(uid136SF2(2, 1, high, low, true))); err != nil {
		t.Fatal(err)
	} else if sf.BitsPerSample != 16 || sf.Has24BitSamples() {
		t.Fatal("SoundFont 2.1 must not enable sm24")
	}

	// Length mismatch: self-consistent chunk with wrong byte count -> 16 bit
	// fallback while RIFF framing stays intact.
	wrongLow := make([]byte, n+4)
	if sf, err := meltysynth.NewSoundFont(bytes.NewReader(uid136SF2(2, 4, high, wrongLow, true))); err != nil {
		t.Fatalf("length mismatch should fall back, not error: %v", err)
	} else if sf.Has24BitSamples() || sf.BitsPerSample != 16 || len(sf.Presets) != 1 {
		t.Fatalf("length mismatch: bits=%d has24=%v presets=%d", sf.BitsPerSample, sf.Has24BitSamples(), len(sf.Presets))
	}
}

// Truncated / negative / gigantic length fields must produce a deterministic
// error quickly and must never allocate the declared amount.
func TestUID136SF24BadLengths(t *testing.T) {
	info := uid136List("INFO", uid136Chunk("ifil", uid136Version(2, 4)))

	withRIFF := func(lists ...[]byte) []byte {
		var body bytes.Buffer
		body.Write(info)
		for _, l := range lists {
			body.Write(l)
		}
		var riff bytes.Buffer
		riff.WriteString("RIFF")
		_ = binary.Write(&riff, binary.LittleEndian, int32(4+body.Len()))
		riff.WriteString("sfbk")
		riff.Write(body.Bytes())
		return riff.Bytes()
	}

	// Negative sm24 size.
	negSDTA := uid136List("sdta", uid136RawChunkHeader("sm24", -1))
	if _, err := meltysynth.NewSoundFont(bytes.NewReader(withRIFF(negSDTA))); err == nil {
		t.Fatal("negative chunk size must be rejected")
	}

	// Huge declared size with no payload.
	var huge bytes.Buffer
	huge.WriteString("LIST")
	_ = binary.Write(&huge, binary.LittleEndian, int32(0x7fffffff))
	huge.WriteString("sdta")
	huge.Write(uid136RawChunkHeader("sm24", 0x7fffff00))
	if _, err := meltysynth.NewSoundFont(bytes.NewReader(withRIFF(huge.Bytes()))); err == nil {
		t.Fatal("truncated huge chunk must return an error")
	}
}

// Illegal sample ranges must not panic the renderer.
func TestUID136SF24IllegalRangeNoPanic(t *testing.T) {
	const n = 8
	high := make([]int16, n)
	low := make([]byte, n)
	sfBytes := uid136SF2(2, 4, high, low, true)
	sf, err := meltysynth.NewSoundFont(bytes.NewReader(sfBytes))
	if err != nil {
		t.Fatal(err)
	}
	sf.SampleHeaders[0].Start = 1_000_000
	sf.SampleHeaders[0].End = 2_000_000

	settings := meltysynth.NewSynthesizerSettings(44100)
	settings.EnableReverbAndChorus = false
	settings.Enable24BitAudio = true
	synth, err := meltysynth.NewSynthesizer(sf, settings)
	if err != nil {
		t.Fatal(err)
	}
	synth.NoteOn(0, 60, 127)
	left := make([]float32, 128)
	right := make([]float32, 128)
	synth.Render(left, right) // must return, not panic
}
