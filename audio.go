package main

import (
	"encoding/binary"
	"io"
	"math"
	"math/rand"
	"os/exec"
	"sync"
)

// ──────────────────────────────────────────────────────────────
//  Chiptune Synthesizer — Amiga-style tracker music
//
//  Pure Go synthesis: square, triangle, noise, and pulse
//  oscillators mixed into PCM samples. Piped to aplay/paplay
//  for zero-dependency audio playback on Linux.
//
//  4 channels (classic MOD layout):
//    CH1: Lead melody (pulse wave, variable duty cycle)
//    CH2: Arpeggio/harmony (square wave)
//    CH3: Bass (triangle wave)
//    CH4: Drums (noise + envelope)
// ──────────────────────────────────────────────────────────────

const (
	sampleRate = 44100
	bpm        = 125
	rowsPerBeat = 4
	rowRate     = float64(bpm) * float64(rowsPerBeat) / 60.0 // rows per second
	samplesPerRow = int(float64(sampleRate) / rowRate)
)

// ── Note frequencies (C-2 to B-5, Amiga octave naming) ───────

var noteFreqs = map[string]float64{
	"C-2": 65.41, "C#2": 69.30, "D-2": 73.42, "D#2": 77.78,
	"E-2": 82.41, "F-2": 87.31, "F#2": 92.50, "G-2": 98.00,
	"G#2": 103.83, "A-2": 110.00, "A#2": 116.54, "B-2": 123.47,

	"C-3": 130.81, "C#3": 138.59, "D-3": 146.83, "D#3": 155.56,
	"E-3": 164.81, "F-3": 174.61, "F#3": 185.00, "G-3": 196.00,
	"G#3": 207.65, "A-3": 220.00, "A#3": 233.08, "B-3": 246.94,

	"C-4": 261.63, "C#4": 277.18, "D-4": 293.66, "D#4": 311.13,
	"E-4": 329.63, "F-4": 349.23, "F#4": 369.99, "G-4": 392.00,
	"G#4": 415.30, "A-4": 440.00, "A#4": 466.16, "B-4": 493.88,

	"C-5": 523.25, "C#5": 554.37, "D-5": 587.33, "D#5": 622.25,
	"E-5": 659.26, "F-5": 698.46, "F#5": 739.99, "G-5": 783.99,
	"G#5": 830.61, "A-5": 880.00, "A#5": 932.33, "B-5": 987.77,

	"---": 0, // silence / note off
}

// ── Tracker pattern ──────────────────────────────────────────

type patternRow struct {
	ch1, ch2, ch3, ch4 string // note names ("C-4", "---" for silence)
}

// Boot tune: dreamy ascending arpeggios → driving pattern
// 32 rows = 2 bars at 125 BPM, loops once = ~8 seconds total
var bootPattern = []patternRow{
	// Bar 1: Ethereal intro — arpeggiated C minor chord rising
	{"C-4", "---", "C-2", "---"},  // 0
	{"---", "D#4", "---", "---"},  // 1
	{"G-4", "---", "---", "HH"},   // 2
	{"---", "D#4", "---", "---"},  // 3
	{"C-5", "---", "C-3", "KK"},   // 4  kick
	{"---", "G-4", "---", "---"},  // 5
	{"D#4", "---", "---", "HH"},   // 6
	{"---", "C-4", "---", "---"},  // 7
	{"G-4", "---", "G-2", "---"},  // 8
	{"---", "D#4", "---", "HH"},   // 9
	{"C-5", "---", "---", "---"},  // 10
	{"---", "G-4", "---", "---"},  // 11
	{"D#5", "---", "D#2", "KK"},   // 12 kick
	{"---", "C-5", "---", "---"},  // 13
	{"G-4", "---", "---", "HH"},   // 14
	{"---", "D#4", "---", "---"},  // 15

	// Bar 2: Building — add bass motion, driving rhythm
	{"C-5", "---", "C-3", "KK"},   // 16 kick
	{"---", "G-4", "---", "HH"},   // 17
	{"D#5", "---", "---", "---"},  // 18
	{"---", "C-5", "D#3", "HH"},   // 19
	{"G-5", "---", "---", "KK"},   // 20 kick
	{"---", "D#5", "---", "---"},  // 21
	{"C-5", "---", "G-2", "HH"},   // 22
	{"---", "G-4", "---", "---"},  // 23
	{"D#5", "---", "A#2", "KK"},   // 24 kick
	{"---", "C-5", "---", "HH"},   // 25
	{"G-5", "---", "---", "---"},  // 26
	{"---", "D#5", "---", "HH"},   // 27
	{"C-5", "---", "F-2", "KK"},   // 28 kick — resolving
	{"---", "G-4", "---", "HH"},   // 29
	{"D#4", "---", "---", "---"},  // 30
	{"C-4", "---", "C-2", "HH"},   // 31 — back to root
}

// ── Oscillators ──────────────────────────────────────────────

func oscSquare(phase float64) float64 {
	if math.Mod(phase, 1.0) < 0.5 {
		return 1.0
	}
	return -1.0
}

func oscPulse(phase, duty float64) float64 {
	if math.Mod(phase, 1.0) < duty {
		return 1.0
	}
	return -1.0
}

func oscTriangle(phase float64) float64 {
	p := math.Mod(phase, 1.0)
	if p < 0.5 {
		return 4.0*p - 1.0
	}
	return 3.0 - 4.0*p
}

func oscNoise() float64 {
	return rand.Float64()*2.0 - 1.0
}

// ── Channel state ────────────────────────────────────────────

type channel struct {
	freq     float64
	phase    float64
	volume   float64
	envDecay float64 // per-sample volume decay (0.999 = slow, 0.99 = fast)
}

func (ch *channel) trigger(note string, vol float64, decay float64) {
	f, ok := noteFreqs[note]
	if !ok || note == "---" {
		return
	}
	ch.freq = f
	ch.volume = vol
	ch.envDecay = decay
	ch.phase = 0
}

func (ch *channel) advance() {
	ch.phase += ch.freq / float64(sampleRate)
	ch.volume *= ch.envDecay
	if ch.volume < 0.001 {
		ch.volume = 0
	}
}

// ── Synthesizer ──────────────────────────────────────────────

type chipSynth struct {
	lead    channel // CH1: pulse lead
	arp     channel // CH2: square arp
	bass    channel // CH3: triangle bass
	noise   channel // CH4: noise drums
	row     int
	sample  int
	pattern []patternRow
}

func newChipSynth() *chipSynth {
	return &chipSynth{
		pattern: bootPattern,
		lead:    channel{envDecay: 0.9999},
		arp:     channel{envDecay: 0.9997},
		bass:    channel{envDecay: 0.9999},
		noise:   channel{envDecay: 0.999},
	}
}

func (s *chipSynth) renderSamples(buf []int16) {
	for i := range buf {
		// Trigger notes at row boundaries
		if s.sample%samplesPerRow == 0 {
			row := s.pattern[s.row%len(s.pattern)]

			if row.ch1 != "---" {
				s.lead.trigger(row.ch1, 0.3, 0.9998)
			}
			if row.ch2 != "---" {
				s.arp.trigger(row.ch2, 0.2, 0.9996)
			}
			if row.ch3 != "---" {
				s.bass.trigger(row.ch3, 0.4, 0.9999)
			}

			// Drums
			switch row.ch4 {
			case "KK": // kick
				s.noise.freq = 60
				s.noise.volume = 0.45
				s.noise.envDecay = 0.997
				s.noise.phase = 0
			case "HH": // hi-hat
				s.noise.freq = 8000
				s.noise.volume = 0.12
				s.noise.envDecay = 0.993
				s.noise.phase = 0
			}

			s.row++
		}

		// Mix channels
		mix := 0.0

		// CH1: pulse lead with PWM (duty cycle oscillates for fatness)
		if s.lead.volume > 0.001 {
			duty := 0.3 + 0.15*math.Sin(float64(s.sample)*0.0003)
			mix += oscPulse(s.lead.phase, duty) * s.lead.volume
		}

		// CH2: square arp
		if s.arp.volume > 0.001 {
			mix += oscSquare(s.arp.phase) * s.arp.volume
		}

		// CH3: triangle bass
		if s.bass.volume > 0.001 {
			mix += oscTriangle(s.bass.phase) * s.bass.volume
		}

		// CH4: noise (kick uses pitched-down noise, hi-hat uses high noise)
		if s.noise.volume > 0.001 {
			n := oscNoise() * s.noise.volume
			// Kick: add a sine thump that pitches down
			if s.noise.freq < 200 {
				kickPhase := float64(s.sample) * s.noise.freq / float64(sampleRate)
				n += math.Sin(kickPhase*2*math.Pi) * s.noise.volume * 1.5
				s.noise.freq *= 0.9997 // pitch down
			}
			mix += n
		}

		// Advance all oscillators
		s.lead.advance()
		s.arp.advance()
		s.bass.advance()
		s.noise.advance()

		s.sample++

		// Soft clip
		if mix > 1.0 {
			mix = 1.0
		} else if mix < -1.0 {
			mix = -1.0
		}

		// Convert to 16-bit PCM
		buf[i] = int16(mix * 28000) // ~85% of max to avoid clipping
	}
}

// ── Audio player ─────────────────────────────────────────────

// AudioPlayer manages background audio playback during boot.
type AudioPlayer struct {
	cmd    *exec.Cmd
	writer io.WriteCloser
	done   chan struct{}
	mu     sync.Mutex
}

// StartBootMusic begins playing the chiptune boot track.
// Returns nil if no audio output is available (silent fallback).
func StartBootMusic() *AudioPlayer {
	// Try aplay first (ALSA), then paplay (PulseAudio)
	player := tryAplay()
	if player != nil {
		return player
	}
	player = tryPaplay()
	if player != nil {
		return player
	}
	return nil // silent — no audio available
}

func tryAplay() *AudioPlayer {
	// Check if aplay exists
	if _, err := exec.LookPath("aplay"); err != nil {
		return nil
	}

	cmd := exec.Command("aplay",
		"-t", "raw",
		"-f", "S16_LE",
		"-r", "44100",
		"-c", "1",
		"-q", // quiet (no status output)
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}

	// Suppress stderr
	cmd.Stderr = nil
	cmd.Stdout = nil

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil
	}

	ap := &AudioPlayer{
		cmd:    cmd,
		writer: stdin,
		done:   make(chan struct{}),
	}

	go ap.generate()
	return ap
}

func tryPaplay() *AudioPlayer {
	// Check if paplay exists
	if _, err := exec.LookPath("paplay"); err != nil {
		return nil
	}

	cmd := exec.Command("paplay",
		"--format=s16le",
		"--rate=44100",
		"--channels=1",
		"--raw",
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}

	cmd.Stderr = nil
	cmd.Stdout = nil

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil
	}

	ap := &AudioPlayer{
		cmd:    cmd,
		writer: stdin,
		done:   make(chan struct{}),
	}

	go ap.generate()
	return ap
}

func (ap *AudioPlayer) generate() {
	synth := newChipSynth()
	buf := make([]int16, 2048) // ~46ms of audio per chunk
	pcm := make([]byte, len(buf)*2)

	// Generate 2 loops of the pattern (~8 seconds total)
	totalRows := len(bootPattern) * 2
	totalSamples := totalRows * samplesPerRow

	generated := 0
	for generated < totalSamples {
		select {
		case <-ap.done:
			return
		default:
		}

		remaining := totalSamples - generated
		chunkSize := len(buf)
		if chunkSize > remaining {
			chunkSize = remaining
		}

		synth.renderSamples(buf[:chunkSize])

		// Convert to little-endian bytes
		for i := 0; i < chunkSize; i++ {
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(buf[i]))
		}

		if _, err := ap.writer.Write(pcm[:chunkSize*2]); err != nil {
			return // pipe broken, player stopped
		}

		generated += chunkSize
	}

	// Close stdin to signal end of audio
	ap.writer.Close()
}

// Stop kills the audio player.
func (ap *AudioPlayer) Stop() {
	if ap == nil {
		return
	}
	ap.mu.Lock()
	defer ap.mu.Unlock()

	select {
	case <-ap.done:
		return // already stopped
	default:
	}

	close(ap.done)
	ap.writer.Close()
	if ap.cmd.Process != nil {
		ap.cmd.Process.Kill()
	}
	ap.cmd.Wait()
}
