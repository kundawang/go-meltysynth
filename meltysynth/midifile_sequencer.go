package meltysynth

import (
	"math"
	"time"
)

type MidiFileSequencer struct {
	synthesizer *Synthesizer
	midiFile    *MidiFile
	loop        bool

	// The playback position in PCM samples from the beginning of the current
	// loop iteration, i.e. the sample where the next Render call starts.
	samplePos int64

	// The index of the next MIDI event to be dispatched.
	msgIndex int32

	// The position (in PCM samples, relative to the loop start) of each event.
	eventSamples []int64

	// The length of one loop iteration in PCM samples.
	loopLength int64

	// The absolute sample position of the beginning of the current loop
	// iteration. An event at relative position p is at loopOffset + p.
	loopOffset int64
}

func NewMidiFileSequencer(s *Synthesizer) *MidiFileSequencer {
	result := new(MidiFileSequencer)
	result.synthesizer = s
	return result
}

func (seq *MidiFileSequencer) Play(midiFile *MidiFile, loop bool) {
	seq.midiFile = midiFile
	seq.loop = loop

	seq.samplePos = 0
	seq.msgIndex = 0
	seq.loopOffset = 0

	// Convert the event timestamps to absolute PCM sample positions so that
	// events can be dispatched at the exact sample they belong to, regardless
	// of the block size used by the synthesizer and of the Render chunking.
	sampleRate := int64(seq.synthesizer.SampleRate)
	msgLength := int32(len(midiFile.messages))
	seq.eventSamples = make([]int64, msgLength)
	for i := int32(0); i < msgLength; i++ {
		seq.eventSamples[i] = durationToSample(midiFile.times[i], sampleRate)
	}

	if msgLength > 0 {
		seq.loopLength = seq.eventSamples[msgLength-1]
	} else {
		seq.loopLength = 0
	}

	seq.synthesizer.Reset()
}

func (seq *MidiFileSequencer) Stop() {
	seq.midiFile = nil

	seq.synthesizer.Reset()
}

func (seq *MidiFileSequencer) Render(left []float32, right []float32) {
	length := int64(len(left))
	if length == 0 {
		return
	}

	if seq.midiFile == nil {
		// Keep behaving like a plain synthesizer when no song is loaded.
		seq.synthesizer.Render(left, right)
		return
	}

	msgLength := int32(len(seq.midiFile.messages))
	blockSize := int64(seq.synthesizer.BlockSize)

	var wrote int64
	for wrote < length {
		// Handle a pending loop wrap first. Doing this at the top guarantees
		// that every iteration produces at least one sample, so the boundary
		// cannot reduce the chunk length to zero and stall the loop. The block
		// grid is re-aligned to the loop start because samplePos restarts at
		// zero, and the cached block is invalidated. A zero-length song cannot
		// loop.
		if seq.loop && seq.loopLength > 0 && seq.samplePos >= seq.loopLength {
			seq.loopOffset += seq.loopLength
			seq.samplePos = 0
			seq.msgIndex = 0
			seq.synthesizer.invalidateBlock()
			seq.synthesizer.NoteOffAll(false)
		}

		// The block offset is always the position within the current loop
		// iteration modulo the block size. After a wrap it naturally restarts
		// at zero, so the block grid and the loop stay aligned.
		blockOffset := int32(seq.samplePos % blockSize)

		// At the start of a synthesizer block, dispatch every event that
		// belongs to this block and then synthesize it. Each note-on carries
		// its offset inside the block, so the note starts at the exact PCM
		// sample; events sharing one sample keep their stable file order.
		if blockOffset == 0 {
			seq.dispatchBlockEvents(msgLength, blockSize)
			seq.synthesizer.renderBlockAt()
		}

		// Read only the part of the rendered block that the caller asked for,
		// stopping at the block end or, when looping, at the loop boundary. The
		// block stays cached, so arbitrary Render chunking produces
		// bit-identical output.
		chunkEnd := length - wrote
		if remaining := blockSize - int64(blockOffset); remaining < chunkEnd {
			chunkEnd = remaining
		}
		if seq.loop && seq.loopLength > 0 {
			if toBoundary := seq.loopLength - seq.samplePos; toBoundary < chunkEnd {
				chunkEnd = toBoundary
			}
		}
		if chunkEnd <= 0 {
			// Defensive: the wrap at the top always makes progress, but never
			// issue a zero-length read under unexpected state.
			chunkEnd = 1
		}
		seq.synthesizer.readBlockRange(blockOffset, int32(chunkEnd),
			left[wrote:wrote+chunkEnd], right[wrote:wrote+chunkEnd])

		wrote += chunkEnd
		seq.samplePos += chunkEnd
	}
}

// dispatchBlockEvents sends every event whose loop-relative sample position
// falls inside the block beginning at the current block-aligned samplePos.
// Note-on events are given the offset of their sample within the block so that
// they sound at the exact sample; all events are applied in the chronological,
// stable file order. The MIDI file is sorted by time, so scanning stops at the
// first event beyond the block.
func (seq *MidiFileSequencer) dispatchBlockEvents(msgLength int32, blockSize int64) {
	blockStart := seq.samplePos
	blockEnd := blockStart + blockSize

	// When looping, events that belong to the next loop iteration must not be
	// dispatched from a block that straddles the boundary; they will be
	// dispatched from the freshly aligned block after the wrap.
	if seq.loop && seq.loopLength > 0 && blockEnd > seq.loopLength {
		blockEnd = seq.loopLength
	}

	for seq.msgIndex < msgLength {
		eventSample := seq.eventSamples[seq.msgIndex]
		if eventSample >= blockEnd {
			break
		}

		offset := int32(eventSample - blockStart)
		if offset < 0 {
			offset = 0
		}
		seq.synthesizer.setNoteBlockOffset(offset)

		msg := seq.midiFile.messages[seq.msgIndex]
		if msg.getMessageType() == msg_Normal {
			seq.synthesizer.ProcessMidiMessage(int32(msg.channel), int32(msg.command), int32(msg.data1), int32(msg.data2))
		}
		seq.msgIndex++
	}

	seq.synthesizer.setNoteBlockOffset(0)
}

// durationToSample converts a MIDI timestamp to the number of PCM samples
// elapsed since the beginning of the song. The conversion is done in floating
// point and rounded to the nearest sample, which keeps the result precise for
// ordinary songs while safely handling the extremely long durations that a
// tick-based time map can theoretically produce.
func durationToSample(d time.Duration, sampleRate int64) int64 {
	samples := float64(d) * float64(sampleRate) / float64(time.Second)
	samples = math.Round(samples)
	if samples < 0 {
		return 0
	}
	if samples > float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(samples)
}
