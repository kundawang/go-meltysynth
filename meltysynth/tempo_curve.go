package meltysynth

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// TempoPoint associates a BPM value with a MIDI tick.
// The tempo of a TempoCurve is linearly interpolated between consecutive points.
type TempoPoint struct {
	Tick int32
	Bpm  float64
}

// BarTempoPoint associates a BPM value with a 0-based bar index.
// Bar indices are converted to MIDI ticks using the time signatures
// of the MIDI file for which the curve is built.
type BarTempoPoint struct {
	Bar int32
	Bpm float64
}

// TempoCurve is a piecewise-linear tempo (BPM) curve over MIDI ticks.
//
// The tempo is constant at the first point's BPM before the first point,
// linearly interpolated between consecutive points, and constant at the
// last point's BPM after the last point. Since the tempo is specified in
// BPM (beats per minute), the playback speed accelerates through a segment
// whose end BPM is higher than its start BPM.
type TempoCurve struct {
	resolution int32
	ticks      []int32   // Sorted in strictly ascending order.
	bpms       []float64 // All positive and finite.
	seconds    []float64 // The cumulative playback time in seconds at each tick.
}

// NewTempoCurve creates a piecewise-linear tempo curve from the given points.
//
// The resolution is the ticks-per-quarter-note value of the MIDI file the
// curve will be used with (see NewTempoCurveFromBars for a bar-based variant
// that takes the resolution from the MIDI file). The points do not need to
// be sorted; they are sorted by tick. An error is returned if:
//
//   - the resolution is not positive,
//   - no points are given,
//   - a BPM value is zero, negative, NaN, or infinite,
//   - a tick is negative,
//   - two points share the same tick (overlapping points).
func NewTempoCurve(resolution int32, points []TempoPoint) (*TempoCurve, error) {
	if resolution <= 0 {
		return nil, fmt.Errorf("the resolution must be positive, but was %d", resolution)
	}
	if len(points) == 0 {
		return nil, errors.New("the tempo curve requires at least one point")
	}

	sorted := make([]TempoPoint, len(points))
	copy(sorted, points)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Tick < sorted[j].Tick })

	ticks := make([]int32, len(sorted))
	bpms := make([]float64, len(sorted))
	for i, p := range sorted {
		if p.Tick < 0 {
			return nil, fmt.Errorf("the tick must be non-negative, but was %d", p.Tick)
		}
		if !(p.Bpm > 0) || math.IsInf(p.Bpm, 1) {
			return nil, fmt.Errorf("the BPM must be a positive finite value, but was %v", p.Bpm)
		}
		if i > 0 && p.Tick == ticks[i-1] {
			return nil, fmt.Errorf("duplicate tempo points at tick %d", p.Tick)
		}
		ticks[i] = p.Tick
		bpms[i] = p.Bpm
	}

	seconds := make([]float64, len(ticks))
	seconds[0] = secondsAtConstantTempo(resolution, bpms[0], float64(ticks[0]))
	for i := 1; i < len(ticks); i++ {
		seconds[i] = seconds[i-1] + integrateTempoSegment(resolution, bpms[i-1], bpms[i], float64(ticks[i]-ticks[i-1]))
	}

	curve := new(TempoCurve)
	curve.resolution = resolution
	curve.ticks = ticks
	curve.bpms = bpms
	curve.seconds = seconds
	return curve, nil
}

// NewTempoCurveFromBars creates a piecewise-linear tempo curve from points
// specified by 0-based bar indices. The bar indices are converted to MIDI
// ticks using the resolution and the time signature changes of the given
// MIDI file; bars before the first time signature event (or all bars, if
// the file has none) use 4/4. An error is returned if a bar index is
// negative, if a bar maps beyond the MIDI tick range, if two bars map to
// the same tick (overlapping points), or if a BPM value is invalid
// (see NewTempoCurve).
func NewTempoCurveFromBars(midiFile *MidiFile, points []BarTempoPoint) (*TempoCurve, error) {
	tickPoints := make([]TempoPoint, len(points))
	for i, p := range points {
		tick, err := midiFile.barToTick(p.Bar)
		if err != nil {
			return nil, err
		}
		tickPoints[i] = TempoPoint{Tick: tick, Bpm: p.Bpm}
	}
	return NewTempoCurve(int32(midiFile.resolution), tickPoints)
}

// TickToTime converts a MIDI tick to the playback time under this curve.
// Negative ticks are treated as zero. If the resulting time exceeds the
// representable range of time.Duration (about 292 years), it saturates at
// the maximum duration instead of overflowing.
func (curve *TempoCurve) TickToTime(tick int32) time.Duration {
	return secondsToDuration(curve.tickToSeconds(tick))
}

func (curve *TempoCurve) tickToSeconds(tick int32) float64 {
	if tick <= 0 {
		return 0
	}

	ft := float64(tick)
	n := len(curve.ticks)

	if tick <= curve.ticks[0] {
		return secondsAtConstantTempo(curve.resolution, curve.bpms[0], ft)
	}
	if tick >= curve.ticks[n-1] {
		return curve.seconds[n-1] + secondsAtConstantTempo(curve.resolution, curve.bpms[n-1], ft-float64(curve.ticks[n-1]))
	}

	// Find i such that ticks[i] < tick < ticks[i+1].
	i := sort.Search(n, func(j int) bool { return curve.ticks[j] > tick }) - 1
	delta := ft - float64(curve.ticks[i])
	bpm := curve.bpms[i] + delta/float64(curve.ticks[i+1]-curve.ticks[i])*(curve.bpms[i+1]-curve.bpms[i])
	return curve.seconds[i] + integrateTempoSegment(curve.resolution, curve.bpms[i], bpm, delta)
}

// secondsAtConstantTempo returns the time in seconds to advance
// deltaTick ticks at a constant tempo.
func secondsAtConstantTempo(resolution int32, bpm float64, deltaTick float64) float64 {
	return 60 * deltaTick / (float64(resolution) * bpm)
}

// integrateTempoSegment returns the time in seconds to advance deltaTick
// ticks while the BPM linearly moves from bpm0 to bpm1. This is the
// integral of 60 / (resolution * bpm(tick)) over the segment, which has
// a closed form involving the logarithm of the BPM ratio.
func integrateTempoSegment(resolution int32, bpm0 float64, bpm1 float64, deltaTick float64) float64 {
	if bpm0 == bpm1 {
		return secondsAtConstantTempo(resolution, bpm0, deltaTick)
	}
	return 60 * deltaTick / (float64(resolution) * (bpm1 - bpm0)) * math.Log(bpm1/bpm0)
}

const maxDurationInSeconds = float64(math.MaxInt64) / float64(time.Second)

func secondsToDuration(seconds float64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if seconds >= maxDurationInSeconds {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds * float64(time.Second))
}
