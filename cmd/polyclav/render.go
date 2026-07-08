package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/mschulkind-oss/polyclav/internal/audio"
)

// audio-core renders at a fixed 48 kHz (SAMPLE_RATE in audio-core/src/lib.rs).
const renderSampleRate = 48000

// runRender implements `polyclav render` — an offline (no-device) render of a
// short synth tone to a WAV file. It opens no audio device, so it runs
// anywhere (including a hardware-less CI runner); it is the macOS CI
// offline-render gate and a handy local "does the signal path work" check.
func runRender(args []string) int {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	seconds := fs.Float64("seconds", 2.0, "length to render, in seconds")
	out := fs.String("out", "", "output WAV path (required)")
	note := fs.Int("note", 60, "MIDI note to hold (0..127; 60 = middle C)")
	vel := fs.Int("velocity", 100, "MIDI note-on velocity (1..127)")
	engine := fs.String("engine", "minimoog", "native synth engine name")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2 // flag already printed the error + usage
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "render: --out is required")
		fs.Usage()
		return 2
	}
	if *seconds <= 0 {
		fmt.Fprintln(os.Stderr, "render: --seconds must be positive")
		return 2
	}
	if *note < 0 || *note > 127 || *vel < 1 || *vel > 127 {
		fmt.Fprintln(os.Stderr, "render: --note must be 0..127 and --velocity 1..127")
		return 2
	}

	nFrames := int(*seconds * renderSampleRate)
	samples, err := audio.RenderOffline(*engine, byte(*note), byte(*vel), nFrames)
	if err != nil {
		fmt.Fprintf(os.Stderr, "render: %v\n", err)
		return 1
	}
	if err := writeWAV(*out, samples, renderSampleRate); err != nil {
		fmt.Fprintf(os.Stderr, "render: writing %s: %v\n", *out, err)
		return 1
	}
	fmt.Printf("rendered %.2fs (%d frames) of %q note %d -> %s\n",
		*seconds, nFrames, *engine, *note, *out)
	return 0
}

// writeWAV writes interleaved-stereo f32 `samples` as a 16-bit PCM stereo WAV
// (canonical 44-byte RIFF/WAVE header + PCM data), clamping to [-1, 1].
func writeWAV(path string, samples []float32, sampleRate int) error {
	const (
		channels      = 2
		bitsPerSample = 16
		bytesPerSpl   = bitsPerSample / 8
	)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dataLen := len(samples) * bytesPerSpl
	byteRate := sampleRate * channels * bytesPerSpl
	blockAlign := channels * bytesPerSpl

	var h [44]byte
	copy(h[0:4], "RIFF")
	binary.LittleEndian.PutUint32(h[4:8], uint32(36+dataLen))
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	binary.LittleEndian.PutUint32(h[16:20], 16) // PCM fmt chunk size
	binary.LittleEndian.PutUint16(h[20:22], 1)  // audio format: PCM
	binary.LittleEndian.PutUint16(h[22:24], channels)
	binary.LittleEndian.PutUint32(h[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(h[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(h[34:36], bitsPerSample)
	copy(h[36:40], "data")
	binary.LittleEndian.PutUint32(h[40:44], uint32(dataLen))
	if _, err := f.Write(h[:]); err != nil {
		return err
	}

	buf := make([]byte, dataLen)
	for i, s := range samples {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		v := int16(math.Round(float64(s) * 32767))
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	_, err = f.Write(buf)
	return err
}
