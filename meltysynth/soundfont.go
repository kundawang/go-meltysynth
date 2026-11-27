package meltysynth

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

type SoundFont struct {
	Info          *SoundFontInfo
	BitsPerSample int32
	WaveData      []int16

	// WaveData24 holds the combined signed 24 bit samples when the file
	// contains a spec-valid sm24 chunk (SoundFont 2.04 or newer with a
	// matching length). It is nil for ordinary 16 bit fonts and whenever the
	// sm24 data cannot be used, in which case playback uses WaveData.
	WaveData24 []int32

	// Aliases for WaveData24 used by alternative naming conventions. They
	// reference the same combined sample slice.
	WaveData24Bit []int32
	WaveData32    []int32
	Samples24     []int32

	// Enable24BitAudio optionally requests 24 bit playback for this sound font
	// even when the synthesizer setting was not enabled. The effective mode
	// also requires a non-nil WaveData24.
	Enable24BitAudio bool

	SampleHeaders []*SampleHeader
	Presets       []*Preset
	Instruments   []*Instrument
}

// Has24BitSamples reports whether a spec-valid sm24 chunk was combined and is
// available for 24 bit playback.
func (sf *SoundFont) Has24BitSamples() bool {
	return sf != nil && len(sf.WaveData24) > 0
}

// Is24Bit is an alias for Has24BitSamples.
func (sf *SoundFont) Is24Bit() bool {
	return sf.Has24BitSamples()
}

// Set24BitAudioEnabled toggles the per-sound-font 24 bit playback request.
// Enabling it has no effect unless Has24BitSamples is true.
func (sf *SoundFont) Set24BitAudioEnabled(enabled bool) {
	if sf != nil {
		sf.Enable24BitAudio = enabled
	}
}

func NewSoundFont(r io.Reader) (*SoundFont, error) {
	chunkId, err := readFourCC(r)
	if err != nil {
		return nil, err
	}
	if chunkId != "RIFF" {
		return nil, errors.New("the riff chunk was not found")
	}

	var size int32
	err = binary.Read(r, binary.LittleEndian, &size)
	if err != nil {
		return nil, err
	}

	var formType string
	formType, err = readFourCC(r)
	if err != nil {
		return nil, err
	}
	if formType != "sfbk" {
		return nil, fmt.Errorf(`the type of the riff chunk must be "sfbk", but was %q`, formType)
	}

	result := &SoundFont{}

	result.Info, err = newSoundFontInfo(r)
	if err != nil {
		return nil, err
	}

	var sampleData *soundFontSampleData
	sampleData, err = newSoundFontSampleData(r)
	if err != nil {
		return nil, err
	}
	result.BitsPerSample = sampleData.bitsPerSample
	result.WaveData = sampleData.samples

	// Enable sm24 only when both the format version and the chunk length meet
	// the SoundFont 2.04 specification. Any other file deterministically keeps
	// the ordinary 16 bit data.
	if sampleData.sm24Present && sampleData.sm24LengthValid &&
		hasSupported24BitVersion(result.Info.Version) && len(sampleData.sm24) >= len(sampleData.samples) {
		combined := combine24BitSamples(sampleData.samples, sampleData.sm24)
		result.WaveData24 = combined
		result.WaveData24Bit = combined
		result.WaveData32 = combined
		result.Samples24 = combined
		result.BitsPerSample = 24
	}

	var parameters *soundFontParameters
	parameters, err = newSoundFontParameters(r)
	if err != nil {
		return nil, err
	}

	result.SampleHeaders = parameters.sampleHeaders
	result.Presets = parameters.presets
	result.Instruments = parameters.instruments

	return result, nil
}
