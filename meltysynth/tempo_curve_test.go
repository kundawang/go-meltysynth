package meltysynth

import (
	"bytes"
	"encoding/binary"
	"math"
	"sort"
	"testing"
	"time"
)

// midiEvent is a raw track event used by the test MIDI builder.
type midiEvent struct {
	tick int32
	data []byte
}

func evNoteOn(tick int32, channel byte, key byte, velocity byte) midiEvent {
	return midiEvent{tick, []byte{0x90 | channel, key, velocity}}
}

func evNoteOff(tick int32, channel byte, key byte) midiEvent {
	return midiEvent{tick, []byte{0x80 | channel, key, 0}}
}

func evControlChange(tick int32, channel byte, controller byte, value byte) midiEvent {
	return midiEvent{tick, []byte{0xB0 | channel, controller, value}}
}

func evTempo(tick int32, bpm int32) midiEvent {
	microseconds := 60000000 / bpm
	return midiEvent{tick, []byte{0xFF, 0x51, 0x03, byte(microseconds >> 16), byte(microseconds >> 8), byte(microseconds)}}
}

func evTimeSignature(tick int32, numerator byte, denominatorExponent byte) midiEvent {
	return midiEvent{tick, []byte{0xFF, 0x58, 0x04, numerator, denominatorExponent, 24, 8}}
}

func evEndOfTrack(tick int32) midiEvent {
	return midiEvent{tick, []byte{0xFF, 0x2F, 0x00}}
}

func writeVariableLength(buf *bytes.Buffer, value int32) {
	var tmp [5]byte
	i := len(tmp) - 1
	tmp[i] = byte(value & 0x7F)
	for value >>= 7; value > 0; value >>= 7 {
		i--
		tmp[i] = byte((value & 0x7F) | 0x80)
	}
	buf.Write(tmp[i:])
}

// buildMidi creates a format-0 MIDI file from the given events.
func buildMidi(t *testing.T, resolution int16, events []midiEvent) *MidiFile {
	t.Helper()

	sorted := make([]midiEvent, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].tick < sorted[j].tick })

	var track bytes.Buffer
	current := int32(0)
	for _, e := range sorted {
		writeVariableLength(&track, e.tick-current)
		track.Write(e.data)
		current = e.tick
	}

	var buf bytes.Buffer
	buf.WriteString("MThd")
	binary.Write(&buf, binary.BigEndian, int32(6))
	binary.Write(&buf, binary.BigEndian, int16(0)) // Format 0.
	binary.Write(&buf, binary.BigEndian, int16(1)) // One track.
	binary.Write(&buf, binary.BigEndian, resolution)
	buf.WriteString("MTrk")
	binary.Write(&buf, binary.BigEndian, int32(track.Len()))
	buf.Write(track.Bytes())

	midiFile, err := NewMidiFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return midiFile
}

func TestTempoCurveConstantTempo(t *testing.T) {
	curve, err := NewTempoCurve(480, []TempoPoint{{Tick: 0, Bpm: 120}})
	if err != nil {
		t.Fatal(err)
	}

	areEqual(t, curve.TickToTime(0).Seconds(), 0)
	areEqual(t, curve.TickToTime(480).Seconds(), 0.5)
	areEqual(t, curve.TickToTime(1920).Seconds(), 2.0)
}

func TestTempoCurveLinearRamp(t *testing.T) {
	// Accelerate linearly from 60 BPM to 120 BPM over one quarter note (480 ticks).
	curve, err := NewTempoCurve(480, []TempoPoint{
		{Tick: 0, Bpm: 60},
		{Tick: 480, Bpm: 120},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The integral of 60 / (480 * bpm(tick)) over the segment is
	// (60 / 480) * (480 / (120 - 60)) * ln(120 / 60) = ln(2).
	areEqual(t, curve.TickToTime(480).Seconds(), math.Log(2))

	// After the last point the tempo stays at 120 BPM (0.5 s per 480 ticks).
	areEqual(t, curve.TickToTime(960).Seconds(), math.Log(2)+0.5)

	// Before the first point the tempo stays at 60 BPM; tick 0 is time 0.
	areEqual(t, curve.TickToTime(0).Seconds(), 0)
}

func TestTempoCurveFirstPointAfterZero(t *testing.T) {
	// The tempo before the first point is the first point's BPM.
	curve, err := NewTempoCurve(480, []TempoPoint{{Tick: 480, Bpm: 120}})
	if err != nil {
		t.Fatal(err)
	}

	areEqual(t, curve.TickToTime(240).Seconds(), 0.25)
	areEqual(t, curve.TickToTime(480).Seconds(), 0.5)
	areEqual(t, curve.TickToTime(960).Seconds(), 1.0)
}

func TestTempoCurveUnsortedPoints(t *testing.T) {
	curve, err := NewTempoCurve(480, []TempoPoint{
		{Tick: 480, Bpm: 120},
		{Tick: 0, Bpm: 60},
	})
	if err != nil {
		t.Fatal(err)
	}

	areEqual(t, curve.TickToTime(480).Seconds(), math.Log(2))
}

func TestTempoCurveInvalidBpm(t *testing.T) {
	cases := []struct {
		name string
		bpm  float64
	}{
		{"zero", 0},
		{"negative", -120},
		{"nan", math.NaN()},
		{"plusInf", math.Inf(1)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewTempoCurve(480, []TempoPoint{{Tick: 0, Bpm: c.bpm}})
			if err == nil {
				t.Fatalf("expected an error for BPM %v", c.bpm)
			}
		})
	}
}

func TestTempoCurveInvalidInput(t *testing.T) {
	if _, err := NewTempoCurve(0, []TempoPoint{{Tick: 0, Bpm: 120}}); err == nil {
		t.Fatal("expected an error for a non-positive resolution")
	}
	if _, err := NewTempoCurve(480, nil); err == nil {
		t.Fatal("expected an error for an empty point list")
	}
	if _, err := NewTempoCurve(480, []TempoPoint{{Tick: -1, Bpm: 120}}); err == nil {
		t.Fatal("expected an error for a negative tick")
	}
}

func TestTempoCurveOverlappingPoints(t *testing.T) {
	_, err := NewTempoCurve(480, []TempoPoint{
		{Tick: 0, Bpm: 60},
		{Tick: 480, Bpm: 90},
		{Tick: 480, Bpm: 120},
	})
	if err == nil {
		t.Fatal("expected an error for duplicate ticks")
	}
}

func TestTempoCurveTimeOverflow(t *testing.T) {
	// An extremely slow tempo makes the playback time exceed the range of
	// time.Duration; the result must saturate instead of overflowing.
	curve, err := NewTempoCurve(480, []TempoPoint{{Tick: 0, Bpm: 1e-9}})
	if err != nil {
		t.Fatal(err)
	}
	if got := curve.TickToTime(math.MaxInt32); got != time.Duration(math.MaxInt64) {
		t.Fatalf("expected saturation at the maximum duration, but got %v", got)
	}

	// A normal tempo with the maximum tick must still give an exact finite value.
	curve, err = NewTempoCurve(480, []TempoPoint{{Tick: 0, Bpm: 120}})
	if err != nil {
		t.Fatal(err)
	}
	expected := 60.0 * float64(math.MaxInt32) / (480 * 120)
	areEqual(t, curve.TickToTime(math.MaxInt32).Seconds(), expected)
}

func TestTempoCurveLongFile(t *testing.T) {
	// A curve with many segments spanning the whole tick range.
	points := make([]TempoPoint, 1000)
	for i := range points {
		points[i] = TempoPoint{Tick: int32(i) * 2000000, Bpm: 60 + float64(i%7)*20}
	}
	curve, err := NewTempoCurve(480, points)
	if err != nil {
		t.Fatal(err)
	}

	previous := time.Duration(0)
	for tick := int64(0); tick <= math.MaxInt32; tick += 123456789 {
		current := curve.TickToTime(int32(tick))
		if current < previous {
			t.Fatalf("the time must be non-decreasing, but went from %v to %v at tick %d", previous, current, tick)
		}
		if current < 0 {
			t.Fatalf("the time must be non-negative, but got %v at tick %d", current, tick)
		}
		previous = current
	}
}

func TestMidiFileTicksAndTempoRegression(t *testing.T) {
	// Tempo change in the middle: 120 BPM for the first 480 ticks, then 60 BPM.
	midiFile := buildMidi(t, 480, []midiEvent{
		evTempo(0, 120),
		evTempo(480, 60),
		evNoteOn(0, 0, 60, 100),
		evNoteOn(960, 0, 64, 100),
		evEndOfTrack(960),
	})

	// The tempo events must not appear in the merged messages.
	if len(midiFile.messages) != 3 {
		t.Fatalf("expected 3 messages, but got %d", len(midiFile.messages))
	}

	// The ticks must be parallel to the messages.
	expectedTicks := []int32{0, 960, 960}
	for i, tick := range expectedTicks {
		if midiFile.ticks[i] != tick {
			t.Fatalf("ticks[%d]: expected %d, but got %d", i, tick, midiFile.ticks[i])
		}
	}

	// The times computed from the embedded tempo events must be unchanged.
	areEqual(t, midiFile.times[0].Seconds(), 0)
	areEqual(t, midiFile.times[1].Seconds(), 0.5+1.0)
	areEqual(t, midiFile.times[2].Seconds(), 0.5+1.0)

	if midiFile.GetTickLength() != 960 {
		t.Fatalf("expected a tick length of 960, but got %d", midiFile.GetTickLength())
	}
	areEqual(t, midiFile.GetLength().Seconds(), 1.5)
}

func TestMidiFileTimeSignature(t *testing.T) {
	// 4/4 for the first two bars, then 3/4. Resolution is 480 ticks per quarter.
	midiFile := buildMidi(t, 480, []midiEvent{
		evTimeSignature(0, 4, 2),
		evTimeSignature(3840, 3, 2),
		evNoteOn(0, 0, 60, 100),
		evEndOfTrack(3840),
	})

	if len(midiFile.timeSignatures) != 2 {
		t.Fatalf("expected 2 time signatures, but got %d", len(midiFile.timeSignatures))
	}
	ts := midiFile.timeSignatures[1]
	if ts.tick != 3840 || ts.numerator != 3 || ts.denominator != 4 {
		t.Fatalf("unexpected time signature: %+v", ts)
	}

	cases := []struct {
		bar  int32
		tick int32
	}{
		{0, 0},
		{1, 1920},
		{2, 3840},        // The 3/4 section starts here.
		{3, 3840 + 1440}, // 3 quarters of 480 ticks.
		{4, 3840 + 2880},
	}
	for _, c := range cases {
		tick, err := midiFile.barToTick(c.bar)
		if err != nil {
			t.Fatal(err)
		}
		if tick != c.tick {
			t.Fatalf("bar %d: expected tick %d, but got %d", c.bar, c.tick, tick)
		}
	}
}

func TestMidiFileBarToTickDefault(t *testing.T) {
	// Without any time signature event, bars are 4/4.
	midiFile := buildMidi(t, 480, []midiEvent{
		evNoteOn(0, 0, 60, 100),
		evEndOfTrack(0),
	})

	tick, err := midiFile.barToTick(3)
	if err != nil {
		t.Fatal(err)
	}
	if tick != 3*1920 {
		t.Fatalf("expected tick %d, but got %d", 3*1920, tick)
	}
}

func TestMidiFileBarToTickInvalid(t *testing.T) {
	midiFile := buildMidi(t, 480, []midiEvent{
		evNoteOn(0, 0, 60, 100),
		evEndOfTrack(0),
	})

	if _, err := midiFile.barToTick(-1); err == nil {
		t.Fatal("expected an error for a negative bar")
	}
	if _, err := midiFile.barToTick(math.MaxInt32); err == nil {
		t.Fatal("expected an error for a bar beyond the tick range")
	}
}

func TestTempoCurveFromBars(t *testing.T) {
	midiFile := buildMidi(t, 480, []midiEvent{
		evTimeSignature(0, 4, 2),
		evTimeSignature(3840, 3, 2),
		evNoteOn(0, 0, 60, 100),
		evEndOfTrack(3840),
	})

	curve, err := NewTempoCurveFromBars(midiFile, []BarTempoPoint{
		{Bar: 0, Bpm: 60},
		{Bar: 2, Bpm: 120},
		{Bar: 4, Bpm: 240},
	})
	if err != nil {
		t.Fatal(err)
	}

	expectedTicks := []int32{0, 3840, 6720}
	for i, tick := range expectedTicks {
		if curve.ticks[i] != tick {
			t.Fatalf("curve.ticks[%d]: expected %d, but got %d", i, tick, curve.ticks[i])
		}
	}

	// Bars that map to the same tick are overlapping points and must be rejected.
	if _, err := NewTempoCurveFromBars(midiFile, []BarTempoPoint{{Bar: 0, Bpm: 60}, {Bar: 0, Bpm: 120}}); err == nil {
		t.Fatal("expected an error for duplicate bars")
	}
	if _, err := NewTempoCurveFromBars(midiFile, []BarTempoPoint{{Bar: -1, Bpm: 60}}); err == nil {
		t.Fatal("expected an error for a negative bar")
	}
	if _, err := NewTempoCurveFromBars(midiFile, []BarTempoPoint{{Bar: 0, Bpm: 0}}); err == nil {
		t.Fatal("expected an error for a zero BPM")
	}
}
