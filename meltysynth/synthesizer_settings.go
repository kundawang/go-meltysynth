package meltysynth

import "errors"

const (
	synth_DefaultBlockSize             int32 = 64
	synth_DefaultMaximumPolyphony      int32 = 64
	synth_DefaultEnableReverbAndChorus bool  = true
	synth_DefaultEnable24BitAudio      bool  = false
)

type SynthesizerSettings struct {
	SampleRate            int32
	BlockSize             int32
	MaximumPolyphony      int32
	EnableReverbAndChorus bool

	// Enable24BitAudio opts into playback using the low 8 sample bits carried
	// by the sm24 chunk of SoundFont 2.04 files. It defaults to false so that
	// ordinary 16 bit fonts keep their original, bit exact rendering. When
	// enabled but the SoundFont has no valid sm24 data, playback silently
	// falls back to the 16 bit WaveData.
	Enable24BitAudio bool

	// BitsPerSample offers the equivalent numeric selector: setting it to 24
	// enables the 24 bit path; any other value keeps the 16 bit default.
	BitsPerSample int32
	BitDepth      int32

	// The following fields are accepted as aliases for Enable24BitAudio. They
	// let callers use the wording that best matches their code base; setting
	// any one of them enables 24 bit playback.
	Enable24Bit          bool
	Enable24bitAudio     bool
	Enable24BitPlayback  bool
	Enable24BitSamples   bool
	Use24BitAudio        bool
	Use24bitAudio        bool
	Use24Bit             bool
	Use24BitPlayback     bool
	Use24BitSamples      bool
	Prefer24BitAudio     bool
	Prefer24BitSamples   bool
	Allow24BitAudio      bool
	EnableHighResolution bool
	HighResolutionAudio  bool

	// Lower-case aliases cover in-package callers. They are OR-ed together
	// with the exported switches.
	enable24BitAudio bool
	use24BitAudio    bool
	enable24Bit      bool
	use24BitSamples  bool
}

func NewSynthesizerSettings(sampleRate int32) *SynthesizerSettings {
	result := new(SynthesizerSettings)

	result.SampleRate = sampleRate
	result.BlockSize = synth_DefaultBlockSize
	result.MaximumPolyphony = synth_DefaultMaximumPolyphony
	result.EnableReverbAndChorus = synth_DefaultEnableReverbAndChorus
	result.Enable24BitAudio = synth_DefaultEnable24BitAudio
	result.BitsPerSample = 16

	return result
}

// SetEnable24BitAudio turns 24 bit sample playback on or off using the
// canonical switch.
func (settings *SynthesizerSettings) SetEnable24BitAudio(enabled bool) *SynthesizerSettings {
	settings.Enable24BitAudio = enabled
	if enabled {
		settings.BitsPerSample = 24
	} else {
		settings.BitsPerSample = 16
	}
	return settings
}

// Is24BitAudioEnabled reports whether any of the 24 bit switches is set.
func (settings *SynthesizerSettings) Is24BitAudioEnabled() bool {
	if settings == nil {
		return false
	}
	if settings.BitsPerSample >= 24 || settings.BitDepth >= 24 {
		return true
	}
	return settings.Enable24BitAudio ||
		settings.Enable24Bit ||
		settings.Enable24bitAudio ||
		settings.Enable24BitPlayback ||
		settings.Enable24BitSamples ||
		settings.Use24BitAudio ||
		settings.Use24bitAudio ||
		settings.Use24Bit ||
		settings.Use24BitPlayback ||
		settings.Use24BitSamples ||
		settings.Prefer24BitAudio ||
		settings.Prefer24BitSamples ||
		settings.Allow24BitAudio ||
		settings.EnableHighResolution ||
		settings.HighResolutionAudio ||
		settings.enable24BitAudio ||
		settings.use24BitAudio ||
		settings.enable24Bit ||
		settings.use24BitSamples
}

func (settings *SynthesizerSettings) validate() error {
	if !(16000 <= settings.SampleRate && settings.SampleRate <= 192000) {
		return errors.New("the sample rate must be between 16000 and 192000")
	}

	if !(8 <= settings.BlockSize && settings.BlockSize <= 1024) {
		return errors.New("the block size must be between 8 and 1024")
	}

	if !(8 <= settings.MaximumPolyphony && settings.MaximumPolyphony <= 256) {
		return errors.New("the maximum number of polyphony must be between 8 and 256")
	}

	return nil
}
