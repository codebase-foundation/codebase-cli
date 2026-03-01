package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testBootConfig() *Config {
	return &Config{
		APIKey:  "test",
		BaseURL: "http://localhost",
		Model:   "test-model",
		WorkDir: "/tmp",
	}
}

func TestBootPlasmaRender(t *testing.T) {
	m := newBootModel(testBootConfig())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// Run 20 frames — should not panic, should produce output
	for i := 0; i < 20; i++ {
		m, _ = m.Update(demoTickMsg{})
		v := m.View()
		if v == "" {
			t.Fatalf("empty view at frame %d", i)
		}
	}

	if m.frame != 20 {
		t.Errorf("expected frame=20, got %d", m.frame)
	}
}

func TestBootStepAdvancement(t *testing.T) {
	m := newBootModel(testBootConfig())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	for i := 0; i < 4; i++ {
		m, _ = m.Update(bootTickMsg{})
		if !m.steps[i].done {
			t.Errorf("step %d not done after tick", i)
		}
	}
	if !m.done {
		t.Error("boot not marked done after all steps")
	}
}

func TestBootSkipAnimation(t *testing.T) {
	m := newBootModel(testBootConfig())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	for i, step := range m.steps {
		if !step.done {
			t.Errorf("step %d not done after skip", i)
		}
	}
	if !m.done {
		t.Error("boot not done after skip")
	}
}

func TestBootSmallTerminal(t *testing.T) {
	m := newBootModel(testBootConfig())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 20, Height: 8})

	for i := 0; i < 10; i++ {
		m, _ = m.Update(demoTickMsg{})
	}

	v := m.View()
	if v == "" {
		t.Error("empty view on small terminal")
	}
}

func TestBootContainsHalfBlocks(t *testing.T) {
	m := newBootModel(testBootConfig())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// Run a few frames
	for i := 0; i < 3; i++ {
		m, _ = m.Update(demoTickMsg{})
	}

	v := m.View()
	// Should be full of half-block characters
	count := 0
	for _, r := range v {
		if r == '▀' {
			count++
		}
	}
	// At minimum 80*24 = 1920 half blocks (one per cell)
	if count < 100 {
		t.Errorf("expected many ▀ characters, got %d", count)
	}
}

func TestBootFrameAdvance(t *testing.T) {
	m := newBootModel(testBootConfig())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 12})

	// Verify frame counter advances
	for i := 0; i < 50; i++ {
		m, _ = m.Update(demoTickMsg{})
	}
	if m.frame != 50 {
		t.Errorf("expected frame=50, got %d", m.frame)
	}

	// View should still render fine after many frames
	v := m.View()
	if v == "" {
		t.Error("empty view after 50 frames")
	}
}
