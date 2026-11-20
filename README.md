# Go-MeltySynth

Go-MeltySynth is a SoundFont synthesizer written in Go, ported from [MeltySynth for C#](https://github.com/sinshu/meltysynth).



## Features

* Suitable for both real-time and offline synthesis.
* Support for standard MIDI files.
* No dependencies other than the standard library.



## Demo

https://www.youtube.com/watch?v=HLta6pASIFg

[![Youtube video](https://img.youtube.com/vi/HLta6pASIFg/0.jpg)](https://www.youtube.com/watch?v=HLta6pASIFg)



## Installation

```
go get github.com/sinshu/go-meltysynth
```



## Examples

An example code to synthesize a simple chord:

```go
// Load the SoundFont.
sf2, _ := os.Open("TimGM6mb.sf2")
soundFont, _ := meltysynth.NewSoundFont(sf2)
sf2.Close()

// Create the synthesizer.
settings := meltysynth.NewSynthesizerSettings(44100)
synthesizer, _ := meltysynth.NewSynthesizer(soundFont, settings)

// Play some notes (middle C, E, G).
synthesizer.NoteOn(0, 60, 100)
synthesizer.NoteOn(0, 64, 100)
synthesizer.NoteOn(0, 67, 100)

// The output buffer (3 seconds).
length := 3 * settings.SampleRate
left := make([]float32, length)
right := make([]float32, length)

// Render the waveform.
synthesizer.Render(left, right)
```

Another example code to synthesize a MIDI file:

```go
// Load the SoundFont.
sf2, _ := os.Open("TimGM6mb.sf2")
soundFont, _ := meltysynth.NewSoundFont(sf2)
sf2.Close()

// Create the synthesizer.
settings := meltysynth.NewSynthesizerSettings(44100)
synthesizer, _ := meltysynth.NewSynthesizer(soundFont, settings)

// Load the MIDI file.
mid, _ := os.Open("C:\\Windows\\Media\\flourish.mid")
midiFile, _ := meltysynth.NewMidiFile(mid)
mid.Close()

// Create the MIDI sequencer.
sequencer := meltysynth.NewMidiFileSequencer(synthesizer)
sequencer.Play(midiFile, true)

// The output buffer.
length := int(float64(settings.SampleRate) * float64(midiFile.GetLength()) / float64(time.Second))
left := make([]float32, length)
right := make([]float32, length)

// Render the waveform.
sequencer.Render(left, right)
```

To play a MIDI file with a tempo curve (e.g. gradually accelerating from a slow beat), use `PlayWithTempoCurve`. The curve is a piecewise-linear BPM curve specified by MIDI ticks (or by bars with `NewTempoCurveFromBars`), and overrides the tempo events in the file:

```go
// Accelerate linearly from 60 BPM to 240 BPM over the first 4 bars,
// then stay at 240 BPM.
curve, _ := meltysynth.NewTempoCurveFromBars(midiFile, []meltysynth.BarTempoPoint{
    {Bar: 0, Bpm: 60},
    {Bar: 4, Bpm: 240},
})

sequencer := meltysynth.NewMidiFileSequencer(synthesizer)
sequencer.PlayWithTempoCurve(midiFile, curve, false)

// The output buffer.
length := int(float64(settings.SampleRate) * float64(midiFile.GetLengthWithTempoCurve(curve)) / float64(time.Second))
left := make([]float32, length)
right := make([]float32, length)

// Render the waveform.
sequencer.Render(left, right)
```



## Todo

* __Wave synthesis__
    - [x] SoundFont reader
    - [x] Waveform generator
    - [x] Envelope generator
    - [x] Low-pass filter
    - [x] Vibrato LFO
    - [x] Modulation LFO
* __MIDI message processing__
    - [x] Note on/off
    - [x] Bank selection
    - [x] Modulation
    - [x] Volume control
    - [x] Pan
    - [x] Expression
    - [x] Hold pedal
    - [x] Program change
    - [x] Pitch bend
    - [x] Tuning
* __Effects__
    - [x] Reverb
    - [x] Chorus
* __Other things__
    - [x] Standard MIDI file support
    - [x] Performace optimization



## License

Go-MeltySynth is available under [the MIT license](LICENSE.txt).
