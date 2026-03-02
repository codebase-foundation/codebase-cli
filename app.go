package main

import (
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ──────────────────────────────────────────────────────────────
//  Root model — routes between boot and chat screens
// ──────────────────────────────────────────────────────────────

type screen int

const (
	screenBoot screen = iota
	screenChat
)

type appModel struct {
	screen screen
	boot   bootModel
	chat   chatModel
	config *Config
}

func newAppModel(cfg *Config) appModel {
	startScreen := screenBoot
	if os.Getenv("CODEBASE_NOBOOT") != "" {
		startScreen = screenChat
	}
	return appModel{
		screen: startScreen,
		boot:   newBootModel(cfg),
		chat:   newChatModel(cfg),
		config: cfg,
	}
}

func (m appModel) Init() tea.Cmd {
	if m.screen == screenChat {
		return m.chat.Init()
	}
	return m.boot.Init()
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global key handling
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "ctrl+c" && m.screen == screenBoot {
			m.boot.audio.Stop()
			return m, tea.Quit
		}
	}

	// Window size goes to both screens
	if wsMsg, ok := msg.(tea.WindowSizeMsg); ok {
		m.boot.width = wsMsg.Width
		m.boot.height = wsMsg.Height
	}

	switch m.screen {
	case screenBoot:
		switch msg.(type) {
		case bootDoneMsg:
			m.boot.audio.Stop() // stop boot music (nil-safe)
			m.screen = screenChat
			return m, tea.Batch(
				m.chat.Init(),
				func() tea.Msg {
					return tea.WindowSizeMsg{Width: m.boot.width, Height: m.boot.height}
				},
			)
		}
		var cmd tea.Cmd
		m.boot, cmd = m.boot.Update(msg)
		return m, cmd

	case screenChat:
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m appModel) View() string {
	switch m.screen {
	case screenBoot:
		return m.boot.View()
	case screenChat:
		return m.chat.View()
	}
	return ""
}

// Cleanup gracefully shuts down background goroutines and processes.
// Called from main.go's defer chain before terminal restoration.
func (m *appModel) Cleanup() {
	// Stop boot audio if still playing
	m.boot.audio.Stop()

	// Signal agent to stop
	if m.chat.stopCh != nil {
		select {
		case <-m.chat.stopCh:
		default:
			close(m.chat.stopCh)
		}
	}

	// Wait briefly for agent goroutine to finish
	if m.chat.agentDone != nil {
		select {
		case <-m.chat.agentDone:
		case <-time.After(2 * time.Second):
		}
	}

	// Stop chime audio
	if m.chat.chimePlayer != nil {
		m.chat.chimePlayer.Stop()
	}
}
