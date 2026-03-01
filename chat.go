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
	chatIdle       chatState = iota // waiting for user input
	chatPlanning                    // Q&A planning phase
	chatPlanReview                  // reviewing generated plan
	chatStreaming                   // agent is working
	chatDoneFlash                   // brief green flash after completion
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
	toolID  string // tool call ID for matching results to starts
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
	flashFrames  int
	userScrolled bool // true when user has scrolled up — don't auto-scroll
	toolsPending     int       // tools currently running
	toolsDone        int       // tools completed this turn
	lastStreamRebuild time.Time // debounce streaming viewport rebuilds

	// Glue + notifications
	glue          *GlueClient
	notify        *notifyManager
	title         string   // session title from glue
	suggestions   []string // follow-up suggestions
	recentActions []string // recent tool actions for narration
	lastNarration time.Time

	// Planning
	planState *PlanState
}

// Messages
type agentEventMsg AgentEvent
type flashTickMsg struct{}
type narrateTickMsg struct{}
type notifyTickMsg struct{}
type glueResultMsg struct {
	kind        string // "chat", "clarify", "title", "celebrate", "suggest", "narrate"
	text        string
	suggestions []string
}
type planQuestionMsg struct {
	question *PlanQuestion
	done     bool
	summary  string
}
type planGeneratedMsg struct {
	plan string
}
type intentClassifiedMsg struct {
	prompt  string
	intent  Intent
	context []ChatMessage
}

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
		glue:      NewGlueClient(cfg),
		notify:    newNotifyManager(),
	}
}

// recentContext extracts the last few user/assistant exchanges from the agent
// history so that glue models (classify, chat) know what was said.
func (m *chatModel) recentContext() []ChatMessage {
	if m.agent == nil {
		return nil
	}
	// Pull user + assistant messages from agent history (skip system, tool)
	var msgs []ChatMessage
	for _, msg := range m.agent.history {
		if msg.Role == "user" || msg.Role == "assistant" {
			if msg.Content != nil && *msg.Content != "" {
				msgs = append(msgs, ChatMessage{Role: msg.Role, Content: msg.Content})
			}
		}
	}
	// Keep last 6 messages max (3 exchanges) to stay cheap
	if len(msgs) > 6 {
		msgs = msgs[len(msgs)-6:]
	}
	return msgs
}

func (m chatModel) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		m.spinner.Tick,
		tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg { return notifyTickMsg{} }),
	)
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
			if m.state == chatPlanning || m.state == chatPlanReview {
				m.state = chatIdle
				m.planState = nil
				m.input.Placeholder = "describe what you want to build..."
				m.segments = append(m.segments, segment{
					kind: "text",
					text: "\n" + styleWarn.Render("  ■ planning cancelled") + "\n",
				})
				m.rebuildViewport()
				return m, nil
			}
			return m, tea.Quit

		case "enter":
			prompt := strings.TrimSpace(m.input.Value())
			if prompt == "" {
				return m, nil
			}
			m.input.SetValue("")

			switch m.state {
			case chatPlanning:
				// User answering a planning question
				cmds = append(cmds, m.handlePlanAnswer(prompt))

			case chatPlanReview:
				// User reviewing the plan: "go"/"yes" to approve, anything else is revision
				cmds = append(cmds, m.handlePlanReview(prompt))

			case chatIdle:
				// Check for suggestion number selection (e.g. "1", "2", "3")
				if len(m.suggestions) > 0 {
					if idx := parseSuggestionIndex(prompt, len(m.suggestions)); idx >= 0 {
						prompt = m.suggestions[idx]
					}
				}
				m.suggestions = nil

				// Slash commands — intercept before intent classification
				if strings.HasPrefix(prompt, "/") {
					cmd, handled := m.handleCommand(prompt)
					if handled {
						return m, cmd
					}
				}

				// Route through glue intent classification (async — non-blocking)
				hasHistory := m.agent != nil
				ctx := m.recentContext()
				glue := m.glue
				cmds = append(cmds, func() tea.Msg {
					intent := glue.ClassifyIntent(prompt, hasHistory)
					return intentClassifiedMsg{prompt: prompt, intent: intent, context: ctx}
				})

			default:
				// Streaming or flash — ignore enter
			}
		}

	case planQuestionMsg:
		if msg.done {
			// Q&A complete — generate the plan
			m.planState.Done = true
			m.notify.Push(Notification{Type: NotifyProgress, Text: "Generating plan..."})
			m.segments = append(m.segments, segment{
				kind: "text",
				text: styleMuted.Render("  Planning complete. Generating implementation plan...\n\n"),
			})
			m.rebuildViewport()
			glue := m.glue
			ps := m.planState
			cmds = append(cmds, func() tea.Msg {
				plan := glue.GeneratePlan(ps.OriginalPrompt, ps.QAHistory)
				return planGeneratedMsg{plan: plan}
			})
		} else if msg.question != nil {
			// Show the question
			m.planState.CurrentQ = msg.question
			m.planState.QuestionCount++
			qText := FormatQuestion(msg.question, m.planState.QuestionCount)
			m.segments = append(m.segments, segment{
				kind: "text",
				text: "\n" + lipgloss.NewStyle().Foreground(colPurple).Bold(true).Render("  ◆ Planning") + "\n" + qText,
			})
			m.rebuildViewport()
			m.input.Placeholder = "type a number or your answer..."
		}

	case planGeneratedMsg:
		m.planState.Plan = msg.plan
		m.state = chatPlanReview
		m.notify.ClearProgress()
		m.notify.Push(Notification{Type: NotifySuccess, Text: "Plan ready for review"})

		// Show the plan
		m.segments = append(m.segments, segment{
			kind: "text",
			text: "\n" + lipgloss.NewStyle().Foreground(colPurple).Bold(true).Render("  ◆ Implementation Plan") + "\n\n",
		})
		// Indent plan lines
		for _, line := range strings.Split(msg.plan, "\n") {
			m.segments = append(m.segments, segment{
				kind: "text",
				text: "  " + line + "\n",
			})
		}
		m.segments = append(m.segments, segment{
			kind: "text",
			text: "\n" + styleMuted.Render("  Type \"go\" to start building, or describe changes to revise the plan.") + "\n",
		})
		m.input.Placeholder = "go / or describe revisions..."
		m.rebuildViewport()

	case intentClassifiedMsg:
		prompt := msg.prompt
		ctx := msg.context

		switch msg.intent {
		case IntentChat:
			m.segments = append(m.segments, segment{
				kind: "user",
				text: "\n" + styleUserLabel.Render("  ❯ ") + prompt + "\n\n",
			})
			glue := m.glue
			cmds = append(cmds, func() tea.Msg {
				reply := glue.ChatReply(prompt, ctx)
				return glueResultMsg{kind: "chat", text: reply}
			})

		case IntentClarify:
			m.segments = append(m.segments, segment{
				kind: "user",
				text: "\n" + styleUserLabel.Render("  ❯ ") + prompt + "\n\n",
			})
			glue := m.glue
			cmds = append(cmds, func() tea.Msg {
				reply := glue.ClarifyReply(prompt)
				return glueResultMsg{kind: "clarify", text: reply}
			})

		case IntentPlan:
			m.startPlanning(prompt)
			glue := m.glue
			ps := m.planState
			cmds = append(cmds, func() tea.Msg {
				q, done, summary := glue.GenerateQuestion(ps.OriginalPrompt, ps.QAHistory, ps.QuestionCount+1)
				return planQuestionMsg{question: q, done: done, summary: summary}
			})
			if m.title == "" {
				glue := m.glue
				cmds = append(cmds, func() tea.Msg {
					title := glue.GenerateTitle(prompt)
					return glueResultMsg{kind: "title", text: title}
				})
			}

		default: // IntentAgent
			m.startAgent(prompt)
			cmds = append(cmds, m.waitForEvent())
			cmds = append(cmds, tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
				return narrateTickMsg{}
			}))
			if m.title == "" {
				glue := m.glue
				cmds = append(cmds, func() tea.Msg {
					title := glue.GenerateTitle(prompt)
					return glueResultMsg{kind: "title", text: title}
				})
			}
			m.notify.Push(Notification{
				Type: NotifyInfo,
				Text: "Starting agent...",
			})
		}
		m.rebuildViewport()

	case agentEventMsg:
		cmds = append(cmds, m.handleAgentEvent(AgentEvent(msg)))

	case glueResultMsg:
		switch msg.kind {
		case "chat", "clarify":
			m.segments = append(m.segments, segment{
				kind: "text",
				text: func() string {
					rendered := renderMarkdownText(msg.text, m.width-8)
					var sb strings.Builder
					for _, line := range strings.Split(rendered, "\n") {
						sb.WriteString("  " + line + "\n")
					}
					return sb.String()
				}(),
			})
			m.rebuildViewport()

		case "title":
			if msg.text != "" {
				m.title = msg.text
			}

		case "narrate":
			if msg.text != "" {
				m.notify.Push(Notification{
					Type: NotifyProgress,
					Text: msg.text,
				})
			}

		case "celebrate":
			if msg.text != "" {
				m.notify.Push(Notification{
					Type: NotifyCelebrate,
					Text: msg.text,
				})
			}

		case "suggest":
			m.suggestions = msg.suggestions
			m.rebuildViewport()
		}

	case narrateTickMsg:
		if m.state == chatStreaming && len(m.recentActions) > 0 &&
			time.Since(m.lastNarration) > 12*time.Second {
			m.lastNarration = time.Now()
			actions := make([]string, len(m.recentActions))
			copy(actions, m.recentActions)
			glue := m.glue
			cmds = append(cmds, func() tea.Msg {
				narration := glue.Narrate(actions)
				return glueResultMsg{kind: "narrate", text: narration}
			})
		}
		if m.state == chatStreaming {
			cmds = append(cmds, tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
				return narrateTickMsg{}
			}))
		}

	case notifyTickMsg:
		m.notify.Tick()
		if m.notify.HasActive() {
			m.rebuildViewport()
		}
		cmds = append(cmds, tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
			return notifyTickMsg{}
		}))

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
		// Only re-render for spinner if there are pending tools (avoids 80ms full rebuild)
		if m.state == chatStreaming && m.toolsPending > 0 {
			m.rebuildViewport()
		}
	}

	if m.state == chatIdle || m.state == chatPlanning || m.state == chatPlanReview {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
		// Track whether user scrolled away from bottom
		m.userScrolled = !m.viewport.AtBottom()
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
	// Adjust viewport height for notifications
	notifyH := len(m.notify.active)
	targetH := m.height - 5 - notifyH
	if targetH < 5 {
		targetH = 5
	}
	if m.viewport.Height != targetH {
		m.viewport.Height = targetH
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
			// Indent every line of the block (not just the first)
			for i, line := range strings.Split(block, "\n") {
				if i > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString("  " + line)
			}
			sb.WriteString("\n\n")
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
	if !m.userScrolled {
		m.viewport.GotoBottom()
	}
}

func (m *chatModel) startAgent(prompt string) {
	m.state = chatStreaming
	m.eventCh = make(chan AgentEvent, 64)
	m.stopCh = make(chan struct{})
	m.streaming.Reset()
	m.turns = 0
	m.recentActions = nil
	m.lastNarration = time.Now() // don't narrate immediately
	m.userScrolled = false

	m.segments = append(m.segments, segment{
		kind: "user",
		text: "\n" + styleUserLabel.Render("  ❯ ") + prompt + "\n\n",
	})
	m.rebuildViewport()

	// Reuse existing agent for conversation continuity, or create new one
	if m.agent == nil {
		client := NewLLMClient(m.config.APIKey, m.config.BaseURL, m.config.Model)
		m.agent = NewAgent(client, m.config.WorkDir, m.eventCh, m.stopCh)

		// Only restore session if --resume flag was passed
		if m.config.Resume {
			if session := LoadSession(m.config.WorkDir, m.config.Model); session != nil {
				m.agent.history = session.History
				m.tokens = session.Tokens
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleMuted.Render("  Session restored from previous conversation.\n\n"),
				})
			}
		}
	} else {
		m.agent.events = m.eventCh
		m.agent.stopCh = m.stopCh
	}

	go func() {
		m.agent.Run(prompt)
		close(m.eventCh)
	}()
}

// startPlanning enters the Q&A planning phase.
func (m *chatModel) startPlanning(prompt string) {
	m.state = chatPlanning
	m.planState = &PlanState{
		OriginalPrompt: prompt,
	}
	m.input.Placeholder = "type a number or your answer..."

	m.segments = append(m.segments, segment{
		kind: "user",
		text: "\n" + styleUserLabel.Render("  ❯ ") + prompt + "\n\n",
	})
	m.notify.Push(Notification{
		Type: NotifyInfo,
		Text: "Entering planning mode...",
	})
	m.rebuildViewport()
}

// handlePlanAnswer processes the user's answer to a planning question.
func (m *chatModel) handlePlanAnswer(input string) tea.Cmd {
	if m.planState == nil || m.planState.CurrentQ == nil {
		return nil
	}

	answer := ParseAnswer(input, m.planState.CurrentQ)
	if answer == "" {
		return nil
	}

	// "Start building" — skip remaining questions, go to plan generation
	if answer == AnswerStartBuilding {
		m.segments = append(m.segments, segment{
			kind: "user",
			text: "  " + styleUserLabel.Render("  → ") + "Start building\n",
		})
		m.planState.CurrentQ = nil
		m.planState.Done = true
		m.rebuildViewport()

		// If we have any Q&A, generate a plan; otherwise go straight to agent
		if len(m.planState.QAHistory) > 0 {
			m.notify.Push(Notification{Type: NotifyProgress, Text: "Generating plan..."})
			glue := m.glue
			ps := m.planState
			return func() tea.Msg {
				plan := glue.GeneratePlan(ps.OriginalPrompt, ps.QAHistory)
				return planGeneratedMsg{plan: plan}
			}
		}

		// No Q&A at all — skip plan, go straight to agent
		m.segments = append(m.segments, segment{
			kind: "text",
			text: styleMuted.Render("  Skipping plan. Starting agent...\n\n"),
		})
		m.input.Placeholder = "describe what you want to build..."
		original := m.planState.OriginalPrompt
		m.planState = nil
		m.startAgent(original)
		return tea.Batch(
			m.waitForEvent(),
			tea.Tick(10*time.Second, func(t time.Time) tea.Msg { return narrateTickMsg{} }),
		)
	}

	// Show the answer
	m.segments = append(m.segments, segment{
		kind: "user",
		text: "  " + styleUserLabel.Render("  → ") + answer + "\n",
	})

	// Record in Q&A history
	m.planState.QAHistory = append(m.planState.QAHistory, QAPair{
		Question: m.planState.CurrentQ.Question,
		Answer:   answer,
	})
	m.planState.CurrentQ = nil
	m.rebuildViewport()

	// Ask next question
	glue := m.glue
	ps := m.planState
	return func() tea.Msg {
		q, done, summary := glue.GenerateQuestion(ps.OriginalPrompt, ps.QAHistory, ps.QuestionCount+1)
		return planQuestionMsg{question: q, done: done, summary: summary}
	}
}

// handlePlanReview processes user input during plan review.
func (m *chatModel) handlePlanReview(input string) tea.Cmd {
	if m.planState == nil {
		return nil
	}

	lower := strings.ToLower(strings.TrimSpace(input))

	// Approve: start the agent with the plan
	if lower == "go" || lower == "yes" || lower == "y" || lower == "ok" || lower == "approve" || lower == "start" {
		m.segments = append(m.segments, segment{
			kind: "text",
			text: styleMuted.Render("  Plan approved. Starting build...\n\n"),
		})
		m.input.Placeholder = "describe what you want to build..."

		// Build enriched prompt from the plan
		enrichedPrompt := BuildPlanPrompt(m.planState.OriginalPrompt, m.planState.Plan, m.planState.QAHistory)
		m.planState = nil

		// Start agent with the enriched prompt
		m.startAgent(enrichedPrompt)
		return tea.Batch(
			m.waitForEvent(),
			tea.Tick(10*time.Second, func(t time.Time) tea.Msg { return narrateTickMsg{} }),
		)
	}

	// Skip planning: just run the original prompt directly
	if lower == "skip" {
		m.segments = append(m.segments, segment{
			kind: "text",
			text: styleMuted.Render("  Skipping plan. Starting agent directly...\n\n"),
		})
		m.input.Placeholder = "describe what you want to build..."

		original := m.planState.OriginalPrompt
		m.planState = nil
		m.startAgent(original)
		return tea.Batch(
			m.waitForEvent(),
			tea.Tick(10*time.Second, func(t time.Time) tea.Msg { return narrateTickMsg{} }),
		)
	}

	// Anything else is revision feedback
	m.segments = append(m.segments, segment{
		kind: "user",
		text: "\n" + styleUserLabel.Render("  ❯ ") + input + "\n\n",
	})
	m.notify.Push(Notification{Type: NotifyProgress, Text: "Revising plan..."})
	m.rebuildViewport()

	glue := m.glue
	currentPlan := m.planState.Plan
	return func() tea.Msg {
		revised := glue.RevisePlan(currentPlan, input)
		return planGeneratedMsg{plan: revised}
	}
}

func (m *chatModel) handleAgentEvent(evt AgentEvent) tea.Cmd {
	// Guard: ignore events if we're no longer streaming (e.g. after ctrl+c)
	if m.state != chatStreaming && evt.Type != EventDone {
		return nil
	}

	switch evt.Type {
	case EventTextDelta:
		m.streaming.WriteString(evt.Text)
		// Debounce: only rebuild viewport at most every 50ms during streaming
		if time.Since(m.lastStreamRebuild) > 50*time.Millisecond {
			m.rebuildViewport()
			m.lastStreamRebuild = time.Now()
		}
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
		m.toolsPending++
		m.segments = append(m.segments, segment{
			kind: "tool",
			tool: &toolSegment{
				name:   evt.Tool,
				args:   evt.Args,
				state:  "pending",
				toolID: evt.ToolID,
			},
		})
		// Track for narration
		action := evt.Tool
		if evt.Args != nil {
			if p, ok := evt.Args["path"]; ok {
				if s, ok := p.(string); ok {
					action += " " + s
				}
			}
			if p, ok := evt.Args["command"]; ok {
				if s, ok := p.(string); ok {
					action += " " + s
				}
			}
		}
		m.recentActions = append(m.recentActions, action)
		if len(m.recentActions) > 8 {
			m.recentActions = m.recentActions[len(m.recentActions)-8:]
		}
		m.rebuildViewport()
		return m.waitForEvent()

	case EventToolResult:
		m.toolsPending--
		m.toolsDone++
		// Match by toolID first, then fall back to last pending
		matched := false
		if evt.ToolID != "" {
			for i := len(m.segments) - 1; i >= 0; i-- {
				if m.segments[i].kind == "tool" && m.segments[i].tool.toolID == evt.ToolID {
					state := "success"
					if !evt.Success {
						state = "error"
					}
					m.segments[i].tool.state = state
					m.segments[i].tool.output = evt.Output
					m.segments[i].tool.args = evt.Args
					matched = true
					break
				}
			}
		}
		if !matched {
			// Fallback: find last pending tool segment
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
		}
		m.rebuildViewport()
		return m.waitForEvent()

	case EventUsage:
		m.tokens = evt.Tokens
		return m.waitForEvent()

	case EventDone:
		m.flushStreamingText()
		m.notify.ClearProgress()
		m.toolsPending = 0
		m.toolsDone = 0
		m.files = 0
		if m.agent != nil {
			m.files = m.agent.FilesChanged()
			// Persist session to disk
			SaveSession(m.agent, m.tokens)
		}
		m.state = chatDoneFlash
		m.flashFrames = 1
		m.rebuildViewport()

		// Glue: celebration + follow-up suggestions (in background)
		summary := evt.Text
		files := m.files
		glue := m.glue
		celebrateCmd := func() tea.Msg {
			msg := glue.Celebrate(summary)
			return glueResultMsg{kind: "celebrate", text: msg}
		}
		suggestCmd := func() tea.Msg {
			suggestions := glue.SuggestFollowUps(summary, files)
			return glueResultMsg{kind: "suggest", suggestions: suggestions}
		}

		return tea.Batch(
			tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg { return flashTickMsg{} }),
			celebrateCmd,
			suggestCmd,
			tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return narrateTickMsg{} }),
		)

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
				rendered := renderMarkdownText(text, m.width-8)
				var sb strings.Builder
				for _, line := range strings.Split(rendered, "\n") {
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
	switch m.state {
	case chatStreaming:
		if m.toolsPending > 0 {
			total := m.toolsDone + m.toolsPending
			statusParts = append(statusParts, m.spinner.View()+styleMuted.Render(fmt.Sprintf(" tools %d/%d", m.toolsDone, total)))
		} else {
			statusParts = append(statusParts, m.spinner.View()+styleMuted.Render(" working"))
		}
	case chatPlanning:
		statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colPurple).Render("◆ planning"))
	case chatPlanReview:
		statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colPurple).Render("◆ review"))
	}
	statusRight := strings.Join(statusParts, styleDim.Render(" │ "))

	titleLeft := styleAccentText.Render(" codebase")
	if m.title != "" {
		titleLeft += styleDim.Render(" · ") + styleMuted.Render(m.title)
	}

	gap := m.width - lipgloss.Width(titleLeft) - lipgloss.Width(statusRight) - 6
	// If title is too long, truncate it to fit
	if gap < 1 && m.title != "" {
		maxTitle := m.width - lipgloss.Width(styleAccentText.Render(" codebase")) - lipgloss.Width(statusRight) - lipgloss.Width(styleDim.Render(" · ")) - 10
		if maxTitle > 3 {
			truncated := m.title
			runes := []rune(truncated)
			if len(runes) > maxTitle {
				truncated = string(runes[:maxTitle-3]) + "..."
			}
			titleLeft = styleAccentText.Render(" codebase") + styleDim.Render(" · ") + styleMuted.Render(truncated)
		} else {
			titleLeft = styleAccentText.Render(" codebase")
		}
		gap = m.width - lipgloss.Width(titleLeft) - lipgloss.Width(statusRight) - 6
	}
	if gap < 1 {
		gap = 1
	}
	header := titleLeft + strings.Repeat(" ", gap) + statusRight

	// ── Notifications ────────────────────────────────────────
	notifyBar := m.notify.Render(m.width)

	// ── Scroll indicator ─────────────────────────────────────
	scrollIndicator := ""
	if m.userScrolled && m.viewport.TotalLineCount() > m.viewport.Height {
		pct := 0
		if m.viewport.TotalLineCount()-m.viewport.Height > 0 {
			pct = int(float64(m.viewport.YOffset) / float64(m.viewport.TotalLineCount()-m.viewport.Height) * 100)
		}
		scrollIndicator = styleDim.Render(fmt.Sprintf(" ↑ %d%% — ↓ scroll to resume auto-scroll", pct)) + "\n"
	}

	// ── Body ─────────────────────────────────────────────────
	body := m.viewport.View()

	// ── Frame ────────────────────────────────────────────────
	var frame lipgloss.Style
	switch m.state {
	case chatStreaming:
		frame = styleFrameActive.Width(m.width - 2)
	case chatDoneFlash:
		frame = styleFrameDone.Width(m.width - 2)
	case chatPlanning, chatPlanReview:
		frame = styleFramePlan.Width(m.width - 2)
	default:
		frame = styleFrame.Width(m.width - 2)
	}

	var innerContent string
	if notifyBar != "" {
		innerContent = header + "\n" + notifyBar + scrollIndicator + body
	} else {
		innerContent = header + "\n" + scrollIndicator + body
	}
	framedBody := frame.Render(innerContent)

	// ── Suggestions ──────────────────────────────────────────
	suggestBar := ""
	if len(m.suggestions) > 0 && m.state == chatIdle {
		suggestBar = renderSuggestions(m.suggestions, m.width) + "\n"
	}

	// ── Input ────────────────────────────────────────────────
	inputLine := " " + m.input.View()
	sep := styleDim.Render(" · ")
	var hint string
	switch m.state {
	case chatStreaming:
		hint = styleDim.Render("ctrl+c stop") + sep + styleDim.Render("↑↓ scroll")
	case chatPlanning:
		hint = styleDim.Render("ctrl+c cancel") + sep + styleDim.Render("enter answer")
	case chatPlanReview:
		hint = styleDim.Render("ctrl+c cancel") + sep + styleDim.Render("\"go\" approve") + sep + styleDim.Render("\"skip\" skip")
	default:
		hint = styleDim.Render("ctrl+c quit") + sep + styleDim.Render("/help commands") + sep + styleDim.Render("↑↓ scroll")
	}
	inputGap := m.width - lipgloss.Width(inputLine) - lipgloss.Width(hint) - 2
	if inputGap < 1 {
		inputGap = 1
	}
	inputRow := inputLine + strings.Repeat(" ", inputGap) + hint

	return framedBody + "\n" + suggestBar + inputRow
}

// parseSuggestionIndex checks if input is a suggestion number (1-based).
// Returns 0-based index or -1 if not a valid suggestion number.
func parseSuggestionIndex(input string, count int) int {
	input = strings.TrimSpace(input)
	if len(input) != 1 || input[0] < '1' || input[0] > '9' {
		return -1
	}
	idx := int(input[0]-'0') - 1
	if idx >= count {
		return -1
	}
	return idx
}

