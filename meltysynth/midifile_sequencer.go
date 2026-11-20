package meltysynth

import (
	"math"
	"time"
)

type MidiFileSequencer struct {
	synthesizer *Synthesizer
	midiFile    *MidiFile
	times       []time.Duration
	loop        bool
	blockWrote  int32
	currentTime time.Duration
	msgIndex    int32
	loopIndex   int32
}

func NewMidiFileSequencer(s *Synthesizer) *MidiFileSequencer {
	result := new(MidiFileSequencer)
	result.synthesizer = s
	return result
}

func (seq *MidiFileSequencer) Play(midiFile *MidiFile, loop bool) {
	seq.play(midiFile, midiFile.times, loop)
}

// PlayWithTempoCurve plays the MIDI file with the given tempo curve instead
// of the tempo events in the file. The event times are computed by mapping
// each event's tick through the curve, so notes that span curve boundaries
// are not retriggered; only their timing changes. The curve should be built
// with the same resolution as the MIDI file (see NewTempoCurveFromBars).
func (seq *MidiFileSequencer) PlayWithTempoCurve(midiFile *MidiFile, curve *TempoCurve, loop bool) {
	times := make([]time.Duration, len(midiFile.messages))
	for i := range midiFile.messages {
		times[i] = curve.TickToTime(midiFile.ticks[i])
	}
	seq.play(midiFile, times, loop)
}

func (seq *MidiFileSequencer) play(midiFile *MidiFile, times []time.Duration, loop bool) {
	seq.midiFile = midiFile
	seq.times = times
	seq.loop = loop

	seq.blockWrote = seq.synthesizer.BlockSize

	seq.currentTime = time.Duration(0)
	seq.msgIndex = 0
	seq.loopIndex = 0

	seq.synthesizer.Reset()
}

func (seq *MidiFileSequencer) Stop() {
	seq.midiFile = nil
	seq.times = nil

	seq.synthesizer.Reset()
}

func (seq *MidiFileSequencer) Render(left []float32, right []float32) {
	var wrote int32
	length := int32(len(left))
	for wrote < length {
		if seq.blockWrote == seq.synthesizer.BlockSize {
			seq.processEvents()
			seq.blockWrote = 0
			seq.currentTime += time.Duration(float64(time.Second) * float64(seq.synthesizer.BlockSize) / float64(seq.synthesizer.SampleRate))
		}

		srcRem := seq.synthesizer.BlockSize - seq.blockWrote
		dstRem := length - wrote
		rem := int32(math.Min(float64(srcRem), float64(dstRem)))

		seq.synthesizer.Render(left[wrote:wrote+rem], right[wrote:wrote+rem])

		seq.blockWrote += rem
		wrote += rem
	}
}

func (seq *MidiFileSequencer) processEvents() {
	if seq.midiFile == nil {
		return
	}

	msgLength := int32(len(seq.midiFile.messages))
	for seq.msgIndex < msgLength {
		time := seq.times[seq.msgIndex]
		msg := seq.midiFile.messages[seq.msgIndex]
		if time <= seq.currentTime {
			if msg.getMessageType() == msg_Normal {
				seq.synthesizer.ProcessMidiMessage(int32(msg.channel), int32(msg.command), int32(msg.data1), int32(msg.data2))
			}
			seq.msgIndex++
		} else {
			break
		}
	}

	if seq.msgIndex == msgLength && seq.loop {
		seq.currentTime = 0
		seq.msgIndex = 0
		seq.synthesizer.NoteOffAll(false)
	}
}
