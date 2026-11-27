package meltysynth

import (
	"math"
)

// In this class, fixed-point numbers are used for speed-up.
// A fixed-point number is expressed by Int64, whose lower 24 bits represent the fraction part,
// and the rest represent the integer part.
// For clarity, fixed-point number variables have a suffix "_fp".

const fracBits int32 = 24
const fracUnit int64 = 1 << fracBits
const fpToSample float32 = float32(1) / float32(32768*fracUnit)

// fpToSample24 scales the same fixed-point interpolation when the sample
// values are canonical signed 24 bit integers (full scale 2^23) instead of
// signed 16 bit integers (full scale 2^15). The 24 bit data therefore keeps
// the exact same output amplitude while retaining the sm24 low bits.
const fpToSample24 float32 = float32(1) / float32((1<<23)*fracUnit)

type oscillator struct {
	synthesizer      *Synthesizer
	data             []int16
	data24           []int32
	loopMode         int32
	sampleRate       int32
	sampleStart      int32
	sampleEnd        int32
	startLoop        int32
	endLoop          int32
	rootKey          int32
	tune             float32
	pitchChangeScale float32
	sampleRateRatio  float32
	looping          bool
	position_fp      int64
}

func newOscillator(s *Synthesizer) *oscillator {
	result := new(oscillator)
	result.synthesizer = s
	return result
}

func (o *oscillator) start(data []int16, loopMode int32, sampleRate int32, start int32, end int32, startLoop int32, endLoop int32, rootKey int32, coarseTune int32, fineTune int32, scaleTuning int32) {
	o.data = data
	o.loopMode = loopMode
	o.sampleRate = sampleRate
	o.sampleStart = start
	o.sampleEnd = end
	o.startLoop = startLoop
	o.endLoop = endLoop
	o.rootKey = rootKey

	o.tune = float32(coarseTune) + float32(0.01)*float32(fineTune)
	o.pitchChangeScale = float32(0.01) * float32(scaleTuning)
	o.sampleRateRatio = float32(sampleRate) / float32(o.synthesizer.SampleRate)

	if loopMode == loop_NoLoop {
		o.looping = false
	} else {
		o.looping = true
	}

	o.position_fp = int64(start) << fracBits
}

func (o *oscillator) release() {
	if o.loopMode == loop_LoopUntilNoteOff {
		o.looping = false
	}
}

func (o *oscillator) process(block []float32, pitch float32) bool {
	pitchChange := o.pitchChangeScale*(pitch-float32(o.rootKey)) + o.tune
	pitchRatio := float64(o.sampleRateRatio) * math.Pow(float64(2), float64(pitchChange)/float64(12))
	return o.fillBlock(block, pitchRatio)
}

func (o *oscillator) fillBlock(block []float32, pitchRatio float64) bool {
	pitchRatio_fp := int64(float64(fracUnit) * pitchRatio)

	if o.data24 != nil {
		if o.looping {
			return o.fillBlock24_Continuous(block, pitchRatio_fp)
		}
		return o.fillBlock24_NoLoop(block, pitchRatio_fp)
	}

	if o.looping {
		return o.fillBlock_Continuous(block, pitchRatio_fp)
	} else {
		return o.fillBlock_NoLoop(block, pitchRatio_fp)
	}
}

// safeInt16 returns the sample at index, or zero when the index is outside
// the wave data. It keeps malformed sample headers deterministic instead of
// panicking.
func (o *oscillator) safeInt16(index int32) int64 {
	if 0 <= index && int(index) < len(o.data) {
		return int64(o.data[index])
	}
	return 0
}

func (o *oscillator) fillBlock_NoLoop(block []float32, pitchRatio_fp int64) bool {
	blockLength := len(block)

	for t := 0; t < blockLength; t++ {
		index := int32(o.position_fp >> fracBits)
		if index >= o.sampleEnd {
			if t > 0 {
				for i := t; i < blockLength; i++ {
					block[i] = 0
				}
				return true
			}
			return false
		}

		x1 := o.safeInt16(index)
		x2 := o.safeInt16(index + 1)
		a_fp := o.position_fp & (fracUnit - 1)
		block[t] = fpToSample * float32((x1<<fracBits)+a_fp*(x2-x1))

		o.position_fp += pitchRatio_fp
	}

	return true
}

func (o *oscillator) fillBlock24_NoLoop(block []float32, pitchRatio_fp int64) bool {
	blockLength := len(block)

	for t := 0; t < blockLength; t++ {
		index := int32(o.position_fp >> fracBits)
		if index >= o.sampleEnd {
			if t > 0 {
				for i := t; i < blockLength; i++ {
					block[i] = 0
				}
				return true
			}
			return false
		}

		x1 := o.safeInt32_24(index)
		x2 := o.safeInt32_24(index + 1)
		a_fp := o.position_fp & (fracUnit - 1)
		block[t] = fpToSample24 * float32((x1<<fracBits)+a_fp*(x2-x1))

		o.position_fp += pitchRatio_fp
	}

	return true
}

func (o *oscillator) safeInt32_24(index int32) int64 {
	if 0 <= index && int(index) < len(o.data24) {
		return int64(o.data24[index])
	}
	return 0
}

func (o *oscillator) fillBlock_Continuous(block []float32, pitchRatio_fp int64) bool {
	blockLength := len(block)

	endLoop_fp := int64(o.endLoop) << fracBits

	loopLength := int32(o.endLoop - o.startLoop)
	loopLength_fp := int64(loopLength) << fracBits

	for t := 0; t < blockLength; t++ {
		if o.position_fp >= endLoop_fp {
			o.position_fp -= loopLength_fp
		}

		index1 := int32(o.position_fp >> fracBits)
		index2 := index1 + 1

		if index2 >= o.endLoop {
			index2 -= loopLength
		}

		x1 := o.safeInt16(index1)
		x2 := o.safeInt16(index2)
		a_fp := o.position_fp & (fracUnit - 1)
		block[t] = fpToSample * float32((x1<<fracBits)+a_fp*(x2-x1))

		o.position_fp += pitchRatio_fp
	}

	return true
}

func (o *oscillator) fillBlock24_Continuous(block []float32, pitchRatio_fp int64) bool {
	blockLength := len(block)

	endLoop_fp := int64(o.endLoop) << fracBits

	loopLength := int32(o.endLoop - o.startLoop)
	loopLength_fp := int64(loopLength) << fracBits

	for t := 0; t < blockLength; t++ {
		if o.position_fp >= endLoop_fp {
			o.position_fp -= loopLength_fp
		}

		index1 := int32(o.position_fp >> fracBits)
		index2 := index1 + 1

		if index2 >= o.endLoop {
			index2 -= loopLength
		}

		x1 := o.safeInt32_24(index1)
		x2 := o.safeInt32_24(index2)
		a_fp := o.position_fp & (fracUnit - 1)
		block[t] = fpToSample24 * float32((x1<<fracBits)+a_fp*(x2-x1))

		o.position_fp += pitchRatio_fp
	}

	return true
}
