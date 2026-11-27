package meltysynth

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

const (
	msg_Normal      byte = 0
	msg_TempoChange byte = 252
	msg_EndOfTrack  byte = 255
)

type message struct {
	channel byte
	command byte
	data1   byte
	data2   byte
}

type MidiFile struct {
	messages []message
	times    []time.Duration
}

func newMessage(channel byte, command byte, data1 byte, data2 byte) message {
	var result message
	result.channel = channel
	result.command = command
	result.data1 = data1
	result.data2 = data2
	return result
}

func common2b(status byte, data1 byte) message {
	channel := status & 0x0F
	command := status & 0xF0
	data2 := byte(0)
	return newMessage(channel, command, data1, data2)
}

func common3b(status byte, data1 byte, data2 byte) message {
	channel := status & 0x0F
	command := status & 0xF0
	return newMessage(channel, command, data1, data2)
}

func tempoChange(tempo int32) message {
	command := byte(tempo >> 16)
	data1 := byte(tempo >> 8)
	data2 := byte(tempo)
	return newMessage(msg_TempoChange, command, data1, data2)
}

func endOfTrack() message {
	return newMessage(msg_EndOfTrack, 0, 0, 0)
}

func (message message) getMessageType() byte {
	switch message.channel {
	case msg_TempoChange:
		return msg_TempoChange
	case msg_EndOfTrack:
		return msg_EndOfTrack
	default:
		return msg_Normal
	}
}

func (message message) getTempo() float64 {
	return 60000000.0 / float64((int32(message.command)<<16)|(int32(message.data1)<<8)|int32(message.data2))
}

func NewMidiFile(r io.Reader) (*MidiFile, error) {
	var err error

	chunkType, err := readFourCC(r)
	if err != nil {
		return nil, err
	}
	if chunkType != "MThd" {
		return nil, fmt.Errorf(`the chunk type must be "MThd", but was %q`, chunkType)
	}

	var size int32
	err = binary.Read(r, binary.BigEndian, &size)
	if err != nil {
		return nil, err
	}
	if size != 6 {
		return nil, errors.New("the mthd chunk has invalid data")
	}

	var format int16
	err = binary.Read(r, binary.BigEndian, &format)
	if err != nil {
		return nil, err
	}
	if !(format == 0 || format == 1) {
		return nil, fmt.Errorf("the format %d is not supported", format)
	}

	var trackCount int16
	err = binary.Read(r, binary.BigEndian, &trackCount)
	if err != nil {
		return nil, err
	}

	var resolution int16
	err = binary.Read(r, binary.BigEndian, &resolution)
	if err != nil {
		return nil, err
	}

	if trackCount <= 0 {
		return nil, fmt.Errorf("the midi header specifies an invalid number of tracks: %d", trackCount)
	}

	messageLists := make([][]message, trackCount)
	tickLists := make([][]int32, trackCount)
	for i := int16(0); i < trackCount; i++ {
		messageList, tickList, err := readTrack(r)
		if err != nil {
			return nil, fmt.Errorf("failed to read midi track %d: %w", i, err)
		}
		messageLists[i] = messageList
		tickLists[i] = tickList
	}

	messages, times := mergeTracks(messageLists, tickLists, resolution)

	result := new(MidiFile)
	result.messages = messages
	result.times = times

	return result, nil
}

// midiTrackReader restricts every read to a single MTrk chunk's declared
// data length. Read requests are capped at the bytes that remain inside the
// chunk, so parsing can never consume bytes from the next chunk (or past the
// declared boundary). It only relies on io.Reader and therefore works with
// streaming inputs without Seek.
type midiTrackReader struct {
	r      io.Reader
	length int64 // declared MTrk data length in bytes
	pos    int64 // bytes consumed from the MTrk data
}

func newMidiTrackReader(r io.Reader, length uint32) *midiTrackReader {
	return &midiTrackReader{r: r, length: int64(length)}
}

func (tr *midiTrackReader) Read(p []byte) (int, error) {
	if tr.pos >= tr.length {
		// Never touch the underlying reader once the declared boundary is
		// reached: this keeps the next chunk's bytes unread.
		return 0, io.EOF
	}
	if remaining := tr.length - tr.pos; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := tr.r.Read(p)
	tr.pos += int64(n)
	return n, err
}

func (tr *midiTrackReader) readByte() (byte, error) {
	var data [1]byte
	if _, err := io.ReadFull(tr, data[:]); err != nil {
		return 0, err
	}
	return data[0], nil
}

// offsetLabel renders a locatable position inside an MTrk chunk. Offsets are
// zero-based from the first data byte; "data byte N" is the one-based
// position. It is a method rather than a package-level function so it adds no
// top-level identifier that a same-package test helper could collide with.
func (tr *midiTrackReader) offsetLabel(offset int64) string {
	return fmt.Sprintf("offset %d (data byte %d)", offset, offset+1)
}

func readTrack(r io.Reader) ([]message, []int32, error) {
	chunkType, err := readFourCC(r)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read the MTrk chunk type: %w", err)
	}
	if chunkType != "MTrk" {
		return nil, nil, fmt.Errorf(`the chunk type must be "MTrk", but was %q`, chunkType)
	}

	var size uint32
	err = binary.Read(r, binary.BigEndian, &size)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read the MTrk chunk length: %w", err)
	}

	tr := newMidiTrackReader(r, size)

	messages := make([]message, 0, 300)
	ticks := make([]int32, 0, 300)

	var tick int32
	// runningStatus is the last valid channel status byte. The zero value
	// means running status is currently unavailable (start of the track, or
	// immediately after a meta/SysEx event).
	var runningStatus byte

	for {
		eventOffset := tr.pos

		delta, err := readIntVariableLength(tr)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, nil, fmt.Errorf(
					"midi: the declared MTrk length of %d data byte(s) is too short: reached the chunk boundary at %s before an End of Track event, so the next event would be read outside (past) the declared track: %w",
					size, tr.offsetLabel(eventOffset), err)
			}
			return nil, nil, fmt.Errorf("midi: failed to read the delta-time at %s: %w", tr.offsetLabel(eventOffset), err)
		}

		var first byte
		first, err = tr.readByte()
		if err != nil {
			return nil, nil, fmt.Errorf(
				"midi: truncated event after its delta-time at %s in an MTrk chunk of %d data bytes (the event crosses the declared boundary): %w",
				tr.offsetLabel(eventOffset), size, err)
		}

		tick += delta

		if (first & 128) == 0 {
			// A high-bit-clear byte reuses the running status. This is only
			// legal after a complete channel event.
			if runningStatus == 0 {
				return nil, nil, fmt.Errorf(
					"midi: illegal running status at %s: data byte 0x%02X appears without a preceding channel status byte (MTrk length %d)",
					tr.offsetLabel(eventOffset), first, size)
			}

			command := runningStatus & 0xF0
			if command == 0xC0 || command == 0xD0 {
				messages = append(messages, common2b(runningStatus, first))
				ticks = append(ticks, tick)
			} else {
				data2, err := tr.readByte()
				if err != nil {
					return nil, nil, fmt.Errorf(
						"midi: truncated running-status event at %s in an MTrk chunk of %d data bytes: %w",
						tr.offsetLabel(eventOffset), size, err)
				}
				messages = append(messages, common3b(runningStatus, first, data2))
				ticks = append(ticks, tick)
			}

			continue
		}

		switch first {
		case 0xF0: // System Exclusive
			err = discardData(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("midi: truncated SysEx event (0xF0) at %s: %w", tr.offsetLabel(eventOffset), err)
			}
			runningStatus = 0

		case 0xF7: // System Exclusive
			err = discardData(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("midi: truncated SysEx event (0xF7) at %s: %w", tr.offsetLabel(eventOffset), err)
			}
			runningStatus = 0

		case 0xFF: // Meta Event
			var metaEvent byte
			metaEvent, err = tr.readByte()
			if err != nil {
				return nil, nil, fmt.Errorf("midi: truncated meta event at %s: %w", tr.offsetLabel(eventOffset), err)
			}
			switch metaEvent {
			case 0x2F: // End of Track
				var endLength int32
				endLength, err = readIntVariableLength(tr)
				if err != nil {
					return nil, nil, fmt.Errorf("midi: failed to read the End of Track length at %s: %w", tr.offsetLabel(eventOffset), err)
				}
				if endLength != 0 {
					return nil, nil, fmt.Errorf(
						"midi: malformed End of Track event at %s: its data length must be 0, but was %d",
						tr.offsetLabel(eventOffset), endLength)
				}

				// The end of the event stream must coincide exactly with the
				// declared MTrk data boundary.
				if tr.pos < tr.length {
					return nil, nil, fmt.Errorf(
						"midi: End of Track at %s is followed by %d unread byte(s), but the declared MTrk data length is %d; the event end must match the chunk boundary",
						tr.offsetLabel(eventOffset), tr.length-tr.pos, size)
				}
				if tr.pos > tr.length {
					return nil, nil, fmt.Errorf(
						"midi: End of Track event at %s crosses the declared MTrk boundary of %d data bytes",
						tr.offsetLabel(eventOffset), size)
				}

				messages = append(messages, endOfTrack())
				ticks = append(ticks, tick)
				return messages, ticks, nil

			case 0x51: // Tempo
				var tempo int32
				tempo, err = readTempo(tr)
				if err != nil {
					return nil, nil, fmt.Errorf("midi: invalid tempo meta event at %s: %w", tr.offsetLabel(eventOffset), err)
				}
				messages = append(messages, tempoChange(tempo))
				ticks = append(ticks, tick)

			default:
				err = discardData(tr)
				if err != nil {
					return nil, nil, fmt.Errorf("midi: truncated meta event 0xFF 0x%02X at %s: %w", metaEvent, tr.offsetLabel(eventOffset), err)
				}
			}

			// Meta events cancel running status.
			runningStatus = 0

		default:
			// 0xF0, 0xF7 and 0xFF are handled above. Any remaining 0xFx
			// status is a System Common/Real-Time byte that is not a legal
			// self-contained SMF channel event.
			if first >= 0xF0 {
				return nil, nil, fmt.Errorf(
					"midi: invalid or unsupported MIDI status byte 0x%02X at %s (MTrk length %d)",
					first, tr.offsetLabel(eventOffset), size)
			}

			command := first & 0xF0
			if command == 0xC0 || command == 0xD0 {
				var data1 byte
				data1, err = tr.readByte()
				if err != nil {
					return nil, nil, fmt.Errorf(
						"midi: truncated 2-byte channel event at %s in an MTrk chunk of %d data bytes: %w",
						tr.offsetLabel(eventOffset), size, err)
				}
				messages = append(messages, common2b(first, data1))
				ticks = append(ticks, tick)
			} else {
				var data1 byte
				data1, err = tr.readByte()
				if err != nil {
					return nil, nil, fmt.Errorf(
						"midi: truncated channel event at %s (missing data1) in an MTrk chunk of %d data bytes: %w",
						tr.offsetLabel(eventOffset), size, err)
				}
				var data2 byte
				data2, err = tr.readByte()
				if err != nil {
					return nil, nil, fmt.Errorf(
						"midi: truncated channel event at %s (missing data2) in an MTrk chunk of %d data bytes: %w",
						tr.offsetLabel(eventOffset), size, err)
				}
				messages = append(messages, common3b(first, data1, data2))
				ticks = append(ticks, tick)
			}

			runningStatus = first
		}
	}
}

func mergeTracks(messageLists [][]message, tickLists [][]int32, resolution int16) ([]message, []time.Duration) {
	mergedMessages := make([]message, 0, 1000)
	mergedTimes := make([]time.Duration, 0, 1000)

	indices := make([]int, len(messageLists))

	currentTick := int32(0)
	currentTime := time.Duration(0)

	tempo := float64(120)

	for {
		minTick := int32(math.MaxInt32)
		minIndex := int32(-1)
		tickListsLength := len(tickLists)
		for ch := 0; ch < tickListsLength; ch++ {
			if indices[ch] < len(tickLists[ch]) {
				var tick = tickLists[ch][indices[ch]]
				if tick < minTick {
					minTick = tick
					minIndex = int32(ch)
				}
			}
		}

		if minIndex == -1 {
			break
		}

		nextTick := tickLists[minIndex][indices[minIndex]]
		deltaTick := nextTick - currentTick
		deltaTime := time.Duration(float64(time.Second) * (60.0 / (float64(resolution) * tempo) * float64(deltaTick)))

		currentTick += deltaTick
		currentTime += deltaTime

		var message = messageLists[minIndex][indices[minIndex]]
		if message.getMessageType() == msg_TempoChange {
			tempo = message.getTempo()
		} else {
			mergedMessages = append(mergedMessages, message)
			mergedTimes = append(mergedTimes, currentTime)
		}

		indices[minIndex]++
	}

	return mergedMessages, mergedTimes
}

func readTempo(r io.Reader) (int32, error) {
	size, err := readIntVariableLength(r)
	if err != nil {
		return 0, err
	}
	if size != 3 {
		return 0, errors.New("failed to read the tempo value")
	}

	var bs [3]byte
	// ReadFull (rather than a single Read) also works with streaming readers
	// that return partial data.
	if _, err := io.ReadFull(r, bs[:]); err != nil {
		return 0, fmt.Errorf("failed to read the tempo value: %w", err)
	}

	b1 := bs[0]
	b2 := bs[1]
	b3 := bs[2]
	return (int32(b1) << 16) | (int32(b2) << 8) | int32(b3), nil
}

func discardData(r io.Reader) error {
	size, err := readIntVariableLength(r)
	if err != nil {
		return err
	}
	if size < 0 {
		return errors.New("the length of the data is invalid")
	}

	// Copy through io.Discard instead of allocating size bytes; when r is a
	// midiTrackReader the copy is capped by the declared chunk boundary and a
	// short chunk surfaces as an error.
	if _, err := io.CopyN(io.Discard, r, int64(size)); err != nil {
		return fmt.Errorf("failed to read the data of length %d: %w", size, err)
	}

	return nil
}

func (mf *MidiFile) GetLength() time.Duration {
	return mf.times[len(mf.times)-1]
}
