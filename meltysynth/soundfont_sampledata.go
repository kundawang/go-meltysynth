package meltysynth

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

type soundFontSampleData struct {
	bitsPerSample int32
	samples       []int16

	// sm24 contains the raw least-significant byte counterpart of samples.
	// It is only populated when an sm24 sub-chunk is present and its declared
	// length is compatible with the smpl chunk. The SoundFont version gate is
	// applied later by combine24BitSamples.
	sm24             []byte
	sm24Present      bool
	sm24LengthValid  bool
	sm24DeclaredSize int64
}

func newSoundFontSampleData(r io.Reader) (*soundFontSampleData, error) {
	chunkId, err := readFourCC(r)
	if err != nil {
		return nil, err
	}
	if chunkId != "LIST" {
		return nil, errors.New("the list chunk was not found")
	}

	listSize32, err := readInt32LE(r)
	if err != nil {
		return nil, err
	}
	if listSize32 < 4 {
		return nil, fmt.Errorf("the sdta list chunk has an invalid size %d", listSize32)
	}

	listType, err := readFourCC(r)
	if err != nil {
		return nil, err
	}
	if listType != "sdta" {
		return nil, fmt.Errorf(`the type of the list chunk must be "sdta", but was %q`, listType)
	}

	result := new(soundFontSampleData)

	// budget is the number of bytes declared by the LIST header that follow
	// the "sdta" list type. It is only used as an upper bound for sub-chunk
	// sizes so that a forged length can never drive an unbounded allocation.
	budget := int64(listSize32) - 4

	for budget >= 8 {
		var id string
		id, err = readFourCC(r)
		if err != nil {
			return nil, err
		}
		budget -= 4

		size32, err := readInt32LE(r)
		if err != nil {
			return nil, err
		}
		budget -= 4

		if size32 < 0 {
			return nil, fmt.Errorf("the %q sub-chunk has a negative size %d", id, size32)
		}
		size := int64(size32)

		// The payload must fit inside the enclosing LIST chunk. This rejects
		// absurd length fields before any allocation is made.
		if size > budget {
			return nil, fmt.Errorf("the %q sub-chunk size %d exceeds the sdta list boundary", id, size)
		}

		// ReadAll over a LimitReader grows with the bytes actually present,
		// so a huge declared size paired with a short/truncated stream does
		// not allocate the declared amount.
		payload, err := readChunkPayload(r, size)
		if err != nil {
			return nil, fmt.Errorf("failed to read the %q sub-chunk: %w", id, err)
		}

		budget -= size

		// RIFF chunks are word aligned: an odd sized payload is followed by
		// one pad byte which is not included in the chunk size. Consume it so
		// the next chunk header is read at the right offset.
		if size&1 == 1 {
			if err := skipRIFFPadByte(r); err != nil {
				return nil, err
			}
			// The pad byte may or may not be counted in the enclosing LIST
			// size; only debit the budget when it explicitly accounts for it.
			if budget > 0 {
				budget--
			}
		}

		switch id {
		case "smpl":
			result.bitsPerSample = 16
			result.samples = bytesToInt16Samples(payload)

		case "sm24":
			result.sm24Present = true
			result.sm24DeclaredSize = size
			result.sm24 = payload

			// One least-significant byte per 16 bit sample frame. Accept both
			// the exact length and the even-rounded length used by writers
			// that include the RIFF pad byte in the declared chunk size.
			sampleCount := int64(len(result.samples))
			rounded := sampleCount + (sampleCount & 1)
			result.sm24LengthValid = size == sampleCount || size == rounded

		default:
			return nil, fmt.Errorf("the info list contains an unknown id %q", id)
		}
	}

	if result.samples == nil {
		return nil, errors.New("no valid sample data was found")
	}

	if result.sm24Present && !result.sm24LengthValid {
		// The sm24 chunk cannot be paired with smpl. Drop it so playback
		// deterministically falls back to the 16 bit data.
		result.sm24 = nil
	}

	return result, nil
}

// readChunkPayload reads exactly size bytes of a chunk payload. It never
// pre-allocates size bytes based solely on the header: the returned buffer is
// grown from the data actually read. A short stream results in an error.
func readChunkPayload(r io.Reader, size int64) ([]byte, error) {
	if size == 0 {
		return []byte{}, nil
	}

	data, err := io.ReadAll(io.LimitReader(r, size))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, io.ErrUnexpectedEOF
	}
	return data, nil
}

func skipRIFFPadByte(r io.Reader) error {
	var pad [1]byte
	n, err := io.ReadFull(r, pad[:])
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		// A missing pad at the physical end of the stream is harmless: there
		// is no following chunk to align to.
		return nil
	}
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("failed to read the RIFF alignment pad byte")
	}
	return nil
}

func readInt32LE(r io.Reader) (int32, error) {
	var value int32
	if err := binary.Read(r, binary.LittleEndian, &value); err != nil {
		return 0, err
	}
	return value, nil
}

// bytesToInt16Samples reinterprets little-endian sample bytes as signed 16 bit
// samples. An odd trailing byte (non-spec smpl) is ignored.
func bytesToInt16Samples(payload []byte) []int16 {
	count := len(payload) / 2
	samples := make([]int16, count)
	for i := 0; i < count; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(payload[2*i : 2*i+2]))
	}
	return samples
}

// combine24BitSamples joins the high 16 bit word from smpl with the unsigned
// low 8 bit byte from sm24 into the canonical signed 24 bit sample value
// (range -2^23 .. 2^23-1). low must have the same length as high; extra
// padded bytes are ignored.
func combine24BitSamples(high []int16, low []byte) []int32 {
	count := len(high)
	if len(low) < count {
		count = len(low)
	}
	combined := make([]int32, count)
	for i := 0; i < count; i++ {
		combined[i] = int32(high[i])*256 + int32(low[i])
	}
	return combined
}

// hasSupported24BitVersion reports whether the SoundFont format version is
// recent enough to define the sm24 chunk (SoundFont 2.04 or newer).
func hasSupported24BitVersion(v SoundFontVersion) bool {
	return v.Major > 2 || (v.Major == 2 && v.Minor >= 4)
}
