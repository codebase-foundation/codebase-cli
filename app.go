package main

import (
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ──────────────────────────────────────────────────────────────
//  Root model — routes between setup, boot, and chat screens
// ──────────────────────────────────────────────────────────────

type screen int

const (
	screenBoot  screen = iota
	screenChat
	screenSetup
)

type appModel struct {
	screen screen
	boot   bootModel
	chat   chatModel
	setup  setupModel
	config *Config
}

func newAppModel(cfg *Config) appModel {
	startScreen := screenBoot
	if cfg.NeedsSetup {
		startScreen = screenSetup
	} else if os.Getenv("CODEBASE_NOBOOT") != "" {
		startScreen = screenChat
	}
	return appModel{
		screen: startScreen,
		boot:   newBootModel(cfg),
		chat:   newChatModel(cfg),
		setup:  newSetupModel(),
		config: cfg,
	}
}

func (m appModel) Init() tea.Cmd {
	switch m.screen {
	case screenSetup:
		return m.setup.Init()
	case screenChat:
		return m.chat.Init()
	default:
		return m.boot.Init()
	}
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global key handling
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "ctrl+c" && m.screen == screenBoot {
			m.boot.audio.Stop()
			return m, tea.Quit
		}
	}

	// Window size goes to all screens
	if wsMsg, ok := msg.(tea.WindowSizeMsg); ok {
		m.boot.width = wsMsg.Width
		m.boot.height = wsMsg.Height
	}

	switch m.screen {
	case screenSetup:
		switch msg := msg.(type) {
		case setupDoneMsg:
			// Save config
			if err := saveSavedConfig(msg.config); err != nil {
				// Continue anyway — config is in memory
				_ = err
			}
			// Apply to runtime config
			m.config.APIKey = msg.config.APIKey
			m.config.BaseURL = msg.config.BaseURL
			m.config.Model = msg.config.Model
			m.config.NeedsSetup = false

			// Set glue env vars
			if msg.config.GlueAPIKey != "" {
				os.Setenv("GLUE_API_KEY", msg.config.GlueAPIKey)
			}
			if msg.config.GlueBaseURL != "" {
				os.Setenv("GLUE_BASE_URL", msg.config.GlueBaseURL)
			}
			if msg.config.GlueFastModel != "" {
				os.Setenv("GLUE_FAST_MODEL", msg.config.GlueFastModel)
			}
			if msg.config.GlueSmartModel != "" {
				os.Setenv("GLUE_SMART_MODEL", msg.config.GlueSmartModel)
			}

			// Recreate boot and chat models with updated config
			m.boot = newBootModel(m.config)
			m.chat = newChatModel(m.config)

			// Transition to boot screen
			if os.Getenv("CODEBASE_NOBOOT") != "" {
				m.screen = screenChat
				return m, m.chat.Init()
			}
			m.screen = screenBoot
			return m, m.boot.Init()
		default:
			var cmd tea.Cmd
			m.setup, cmd = m.setup.Update(msg)
			return m, cmd
		}

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
		// Check for setup command trigger
		if setupMsg, ok := msg.(enterSetupMsg); ok {
			_ = setupMsg
			m.setup = newSetupModel()
			m.screen = screenSetup
			return m, m.setup.Init()
		}
		var cmd tea.Cmd
		m.chat, cmd = m.chat.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m appModel) View() string {
	switch m.screen {
	case screenSetup:
		return m.setup.View()
	case screenBoot:
		return m.boot.View()
	case screenChat:
		return m.chat.View()
	}
	return ""
}

// enterSetupMsg triggers the setup wizard from a /setup command.
type enterSetupMsg struct{}

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
