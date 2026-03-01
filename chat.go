package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ──────────────────────────────────────────────────────────────
//  Chat screen — main interaction view
// ──────────────────────────────────────────────────────────────

type chatState int

const (
	chatIdle      chatState = iota // waiting for user input
	chatStreaming                  // agent is working
	chatDoneFlash                 // brief green flash after completion
)

// segment represents a chunk of conversation content.
// Using segments avoids fragile ANSI string replacement.
type segment struct {
	kind string         // "text", "user", "tool", "divider", "error"
	text string         // rendered content (for text/user/divider/error)
	tool *toolSegment   // tool block data (for kind=="tool")
}

type toolSegment struct {
	name    string
	args    map[string]any
	output  string
	state   string // "pending", "success", "error"
}

type chatModel struct {
	config    *Config
	viewport  viewport.Model
	input     textinput.Model
	spinner   spinner.Model
	state     chatState
	width     int
	height    int
	ready     bool
	segments  []segment         // conversation segments
	streaming *strings.Builder   // current streaming text (not yet finalized)
	tokens    TokenUsage
	files     int
	turns     int
	eventCh   chan AgentEvent
	stopCh    chan struct{}
	agent     *Agent
	flashFrames int
}

// Messages
type agentEventMsg AgentEvent
type flashTickMsg struct{}

func newChatModel(cfg *Config) chatModel {
	ti := textinput.New()
	ti.Placeholder = "describe what you want to build..."
	ti.Focus()
	ti.CharLimit = 2000
	ti.Prompt = stylePromptChar.Render("❯ ")
	ti.TextStyle = lipgloss.NewStyle().Foreground(colText)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(colDim)

	s := spinner.New()
	s.Spinner = spinner.Spinner{
		Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		FPS:    80 * time.Millisecond,
	}
	s.Style = lipgloss.NewStyle().Foreground(colAccent)

	// Welcome segment
	welcome := []segment{
		{kind: "text", text: styleMuted.Render("  Welcome to Codebase. Type a prompt to begin.\n")},
	}

	return chatModel{
		config:    cfg,
		input:     ti,
		spinner:   s,
		state:     chatIdle,
		segments:  welcome,
		streaming: &strings.Builder{},
	}
}

func (m chatModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

func (m chatModel) waitForEvent() tea.Cmd {
	ch := m.eventCh
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return agentEventMsg{Type: EventDone, Text: ""}
		}
		return agentEventMsg(evt)
	}
}

func (m chatModel) Update(msg tea.Msg) (chatModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.setupViewport()

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.state == chatStreaming {
				select {
				case <-m.stopCh:
				default:
					close(m.stopCh)
				}
				m.state = chatIdle
				m.flushStreamingText()
				m.segments = append(m.segments, segment{
					kind: "text",
					text: "\n" + styleWarn.Render("  ■ stopped") + "\n",
				})
				m.rebuildViewport()
				return m, nil
			}
			return m, tea.Quit

		case "enter":
			if m.state != chatIdle {
				return m, nil
			}
			prompt := strings.TrimSpace(m.input.Value())
			if prompt == "" {
				return m, nil
			}
			m.input.SetValue("")
			m.startAgent(prompt)
			cmds = append(cmds, m.waitForEvent())
		}

	case agentEventMsg:
		cmds = append(cmds, m.handleAgentEvent(AgentEvent(msg)))

	case flashTickMsg:
		m.flashFrames--
		if m.flashFrames <= 0 {
			m.state = chatIdle
		} else {
			cmds = append(cmds, tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
				return flashTickMsg{}
			}))
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
		// Re-render viewport if we have pending tools (spinner updates)
		if m.state == chatStreaming {
			m.rebuildViewport()
		}
	}

	if m.state == chatIdle {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *chatModel) setupViewport() {
	h := m.height - 5 // frame borders + header + input + padding
	if h < 5 {
		h = 5
	}
	w := m.width - 4
	if w < 20 {
		w = 20
	}
	if !m.ready {
		m.viewport = viewport.New(w, h)
		m.ready = true
	} else {
		m.viewport.Width = w
		m.viewport.Height = h
	}
	m.rebuildViewport()
}

func (m *chatModel) rebuildViewport() {
	if !m.ready {
		return
	}
	var sb strings.Builder
	contentW := m.width - 8

	for _, seg := range m.segments {
		switch seg.kind {
		case "tool":
			block := renderToolBlock(seg.tool.name, seg.tool.args, seg.tool.output, seg.tool.state, contentW)
			// If pending, swap the static spinner with animated one
			if seg.tool.state == "pending" {
				block = strings.Replace(block, "⣾", m.spinner.View(), 1)
			}
			sb.WriteString("  " + block + "\n\n")
		default:
			sb.WriteString(seg.text)
		}
	}

	// Append streaming text
	streamText := m.streaming.String()
	if streamText != "" {
		wrapped := wrapText(streamText, contentW)
		for _, line := range strings.Split(wrapped, "\n") {
			sb.WriteString("  " + line + "\n")
		}
	}

	m.viewport.SetContent(sb.String())
	m.viewport.GotoBottom()
}

func (m *chatModel) startAgent(prompt string) {
	m.state = chatStreaming
	m.eventCh = make(chan AgentEvent, 64)
	m.stopCh = make(chan struct{})
	m.streaming.Reset()
	m.turns = 0

	m.segments = append(m.segments, segment{
		kind: "user",
		text: "\n" + styleUserLabel.Render("  ❯ ") + prompt + "\n\n",
	})
	m.rebuildViewport()

	// Reuse existing agent for conversation continuity, or create new one
	if m.agent == nil {
		client := NewLLMClient(m.config.APIKey, m.config.BaseURL, m.config.Model)
		m.agent = NewAgent(client, m.config.WorkDir, m.eventCh, m.stopCh)
	} else {
		m.agent.events = m.eventCh
		m.agent.stopCh = m.stopCh
	}

	go func() {
		m.agent.Run(prompt)
		close(m.eventCh)
	}()
}

func (m *chatModel) handleAgentEvent(evt AgentEvent) tea.Cmd {
	switch evt.Type {
	case EventTextDelta:
		m.streaming.WriteString(evt.Text)
		m.rebuildViewport()
		return m.waitForEvent()

	case EventTurnStart:
		m.turns = evt.Turn
		if evt.Turn > 1 {
			m.flushStreamingText()
			m.segments = append(m.segments, segment{
				kind: "divider",
				text: "\n" + styleDim.Render(fmt.Sprintf("  ─── turn %d ───", evt.Turn)) + "\n\n",
			})
			m.rebuildViewport()
		}
		return m.waitForEvent()

	case EventToolStart:
		m.flushStreamingText()
		m.segments = append(m.segments, segment{
			kind: "tool",
			tool: &toolSegment{
				name:  evt.Tool,
				args:  evt.Args,
				state: "pending",
			},
		})
		m.rebuildViewport()
		return m.waitForEvent()

	case EventToolResult:
		// Find the last pending tool segment and update it
		for i := len(m.segments) - 1; i >= 0; i-- {
			if m.segments[i].kind == "tool" && m.segments[i].tool.state == "pending" {
				state := "success"
				if !evt.Success {
					state = "error"
				}
				m.segments[i].tool.state = state
				m.segments[i].tool.output = evt.Output
				m.segments[i].tool.args = evt.Args
				break
			}
		}
		m.rebuildViewport()
		return m.waitForEvent()

	case EventUsage:
		m.tokens = evt.Tokens
		return m.waitForEvent()

	case EventDone:
		m.flushStreamingText()
		m.files = 0
		if m.agent != nil {
			m.files = m.agent.FilesChanged()
		}
		m.state = chatDoneFlash
		m.flashFrames = 3
		m.rebuildViewport()
		return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
			return flashTickMsg{}
		})

	case EventError:
		m.flushStreamingText()
		errStr := "unknown error"
		if evt.Error != nil {
			errStr = evt.Error.Error()
		}
		m.segments = append(m.segments, segment{
			kind: "error",
			text: "  " + styleErr.Render("Error: "+errStr) + "\n",
		})
		m.rebuildViewport()
		return m.waitForEvent()
	}

	return nil
}

func (m *chatModel) flushStreamingText() {
	text := m.streaming.String()
	if text != "" {
		m.segments = append(m.segments, segment{
			kind: "text",
			text: func() string {
				wrapped := wrapText(text, m.width-8)
				var sb strings.Builder
				for _, line := range strings.Split(wrapped, "\n") {
					sb.WriteString("  " + line + "\n")
				}
				return sb.String()
			}(),
		})
		m.streaming.Reset()
	}
}

func (m chatModel) View() string {
	if !m.ready {
		return "Initializing..."
	}

	// ── Header ───────────────────────────────────────────────
	modelStr := styleMuted.Render(m.config.Model)
	totalTokens := m.tokens.PromptTokens + m.tokens.CompletionTokens
	var tokenStr string
	if totalTokens >= 1000 {
		tokenStr = styleMuted.Render(fmt.Sprintf("%.1fk tokens", float64(totalTokens)/1000))
	} else {
		tokenStr = styleMuted.Render(fmt.Sprintf("%d tokens", totalTokens))
	}
	fileStr := styleMuted.Render(fmt.Sprintf("%d files", m.files))

	statusParts := []string{modelStr, tokenStr, fileStr}
	if m.state == chatStreaming {
		statusParts = append(statusParts, m.spinner.View()+styleMuted.Render(" working"))
	}
	statusRight := strings.Join(statusParts, styleDim.Render(" │ "))
	titleLeft := styleAccentText.Render(" codebase")

	gap := m.width - lipgloss.Width(titleLeft) - lipgloss.Width(statusRight) - 6
	if gap < 1 {
		gap = 1
	}
	header := titleLeft + strings.Repeat(" ", gap) + statusRight

	// ── Body ─────────────────────────────────────────────────
	body := m.viewport.View()

	// ── Frame ────────────────────────────────────────────────
	var frame lipgloss.Style
	switch m.state {
	case chatStreaming:
		frame = styleFrameActive.Width(m.width - 2)
	case chatDoneFlash:
		frame = styleFrameDone.Width(m.width - 2)
	default:
		frame = styleFrame.Width(m.width - 2)
	}

	framedBody := frame.Render(header + "\n" + body)

	// ── Input ────────────────────────────────────────────────
	inputLine := " " + m.input.View()
	hintAction := "quit"
	if m.state == chatStreaming {
		hintAction = "stop"
	}
	hint := styleDim.Render("ctrl+c " + hintAction)
	inputGap := m.width - lipgloss.Width(inputLine) - lipgloss.Width(hint) - 2
	if inputGap < 1 {
		inputGap = 1
	}
	inputRow := inputLine + strings.Repeat(" ", inputGap) + hint

	return framedBody + "\n" + inputRow
}

var styleAccentText = lipgloss.NewStyle().
	Foreground(colAccent).
	Bold(true)
