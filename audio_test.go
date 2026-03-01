package main

import (
	"math"
	"testing"
)

func TestOscSquare(t *testing.T) {
	// First half of cycle should be +1, second half -1
	if oscSquare(0.1) != 1.0 {
		t.Error("square at 0.1 should be 1.0")
	}
	if oscSquare(0.6) != -1.0 {
		t.Error("square at 0.6 should be -1.0")
	}
}

func TestOscPulse(t *testing.T) {
	if oscPulse(0.1, 0.5) != 1.0 {
		t.Error("pulse at 0.1 duty 0.5 should be 1.0")
	}
	if oscPulse(0.6, 0.5) != -1.0 {
		t.Error("pulse at 0.6 duty 0.5 should be -1.0")
	}
	// Narrow duty cycle
	if oscPulse(0.1, 0.2) != 1.0 {
		t.Error("pulse at 0.1 duty 0.2 should be 1.0")
	}
	if oscPulse(0.3, 0.2) != -1.0 {
		t.Error("pulse at 0.3 duty 0.2 should be -1.0")
	}
}

func TestOscTriangle(t *testing.T) {
	// Our triangle: ramps from -1 (at 0) to +1 (at 0.5) and back
	// phase 0.0 → 4*0 - 1 = -1
	// phase 0.25 → 4*0.25 - 1 = 0
	// phase 0.5 → 3 - 4*0.5 = 1
	// phase 0.75 → 3 - 4*0.75 = 0
	val := oscTriangle(0.0)
	if val != -1.0 {
		t.Errorf("triangle at 0.0 should be -1.0, got %f", val)
	}
	val = oscTriangle(0.25)
	if math.Abs(val-0.0) > 0.01 {
		t.Errorf("triangle at 0.25 should be ~0.0, got %f", val)
	}
	val = oscTriangle(0.5)
	if val != 1.0 {
		t.Errorf("triangle at 0.5 should be 1.0, got %f", val)
	}
	val = oscTriangle(0.75)
	if math.Abs(val-0.0) > 0.01 {
		t.Errorf("triangle at 0.75 should be ~0.0, got %f", val)
	}
}

func TestOscNoise(t *testing.T) {
	// Noise should return values in [-1, 1]
	for i := 0; i < 1000; i++ {
		v := oscNoise()
		if v < -1.0 || v > 1.0 {
			t.Fatalf("noise out of range: %f", v)
		}
	}
}

func TestNoteFreqs(t *testing.T) {
	// A-4 should be 440 Hz (concert pitch)
	if noteFreqs["A-4"] != 440.0 {
		t.Errorf("A-4 should be 440, got %f", noteFreqs["A-4"])
	}
	// Silence note
	if noteFreqs["---"] != 0 {
		t.Error("--- should be 0 Hz")
	}
	// C-4 (middle C)
	if math.Abs(noteFreqs["C-4"]-261.63) > 0.01 {
		t.Errorf("C-4 should be ~261.63, got %f", noteFreqs["C-4"])
	}
}

func TestChannelTrigger(t *testing.T) {
	ch := channel{envDecay: 0.999}
	ch.trigger("A-4", 0.5, 0.999)

	if ch.freq != 440.0 {
		t.Errorf("freq should be 440, got %f", ch.freq)
	}
	if ch.volume != 0.5 {
		t.Errorf("volume should be 0.5, got %f", ch.volume)
	}
	if ch.phase != 0 {
		t.Error("phase should reset to 0")
	}
}

func TestChannelTriggerSilence(t *testing.T) {
	ch := channel{freq: 440, volume: 0.5, envDecay: 0.999}
	ch.trigger("---", 0.5, 0.999)
	// Should not change anything
	if ch.freq != 440 {
		t.Error("silence trigger should not change freq")
	}
}

func TestChannelAdvance(t *testing.T) {
	ch := channel{freq: 440, volume: 1.0, envDecay: 0.999, phase: 0}
	ch.advance()

	if ch.phase == 0 {
		t.Error("phase should advance")
	}
	if ch.volume >= 1.0 {
		t.Error("volume should decay")
	}
}

func TestSynthRender(t *testing.T) {
	synth := newChipSynth()
	buf := make([]int16, 4096)

	// Should not panic
	synth.renderSamples(buf)

	// Check output isn't all zeros (we should have some audio)
	nonZero := 0
	for _, s := range buf {
		if s != 0 {
			nonZero++
		}
	}
	if nonZero == 0 {
		t.Error("rendered audio is all silence — synth isn't generating samples")
	}

	// Check samples are within range (soft-clipped at ~85% of int16 max)
	for i, s := range buf {
		if s > 29000 || s < -29000 {
			t.Errorf("sample %d out of expected range: %d", i, s)
			break
		}
	}
}

func TestSynthPatternLength(t *testing.T) {
	if len(bootPattern) != 32 {
		t.Errorf("boot pattern should be 32 rows, got %d", len(bootPattern))
	}
}

func TestSamplesPerRow(t *testing.T) {
	// At 125 BPM, 4 rows per beat: 125*4/60 = ~8.33 rows/sec
	// 44100 / 8.33 = ~5292 samples per row
	if samplesPerRow < 5000 || samplesPerRow > 6000 {
		t.Errorf("samplesPerRow should be ~5292, got %d", samplesPerRow)
	}
}

func TestAudioPlayerNilStop(t *testing.T) {
	// Stop on nil player should not panic
	var ap *AudioPlayer
	ap.Stop() // should be a no-op
}
