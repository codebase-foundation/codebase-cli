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
	chatPermission                  // waiting for permission approval
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
	inThink           bool      // inside <think> tags (MiniMax reasoning)

	// Permission
	permRequest *PermissionRequest // current pending permission prompt
	permCount   int                // how many permission prompts shown this session
	permChoice  int                // 0=allow, 1=deny, 2=always, 3=trust all

	// Glue + notifications
	glue          *GlueClient
	notify        *notifyManager
	title         string   // session title from glue
	suggestions   []string // follow-up suggestions
	recentActions []string // recent tool actions for narration
	lastNarration time.Time

	// Planning
	planState *PlanState

	// Status narration
	lastNarrationText string // last narration from glue for status bar

	// Cleanup
	chimePlayer *AudioPlayer   // active chime for cleanup on exit
	agentDone   chan struct{}   // closed when agent goroutine returns

	// Task tracking
	tasks *TaskStore

	// Quit guard
	ctrlCPending bool // true after first ctrl+c, reset on timeout or other input
}

// Messages
type agentEventMsg AgentEvent
type flashTickMsg struct{}
type narrateTickMsg struct{}
type ctrlCTimeoutMsg struct{}
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
	if activeTheme.Name == "retro" {
		s.Spinner = spinner.Spinner{
			Frames: []string{"▖", "▘", "▝", "▗", "▚", "▞", "█", "▒", "░", "▒"},
			FPS:    100 * time.Millisecond,
		}
	} else {
		s.Spinner = spinner.Spinner{
			Frames: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
			FPS:    80 * time.Millisecond,
		}
	}
	s.Style = lipgloss.NewStyle().Foreground(colAccent)

	// Welcome segment — check for IDE connection
	welcomeText := "  Welcome to Codebase. Type a prompt to begin.\n"
	if ide := DiscoverIDE(cfg.WorkDir); ide != nil {
		welcomeText = "  Welcome to Codebase. " + styleDim.Render("IDE: "+ide.IDEInfo()) + "\n"
	}
	welcome := []segment{
		{kind: "text", text: styleMuted.Render(welcomeText)},
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
		tasks:     NewTaskStore(),
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
	stop := m.stopCh
	return func() tea.Msg {
		select {
		case evt, ok := <-ch:
			if !ok {
				return agentEventMsg{Type: EventDone, Text: ""}
			}
			return agentEventMsg(evt)
		case <-stop:
			return agentEventMsg{Type: EventDone, Text: "Stopped."}
		}
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
			if m.state == chatPermission {
				// Deny the pending permission and return to streaming
				if m.agent != nil {
					select {
					case m.agent.permCh <- PermissionResponse{Allowed: false}:
					default:
					}
				}
				m.permRequest = nil
				m.state = chatStreaming
				m.input.Placeholder = "describe what you want to build..."
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleWarn.Render("  ✗ denied") + "\n",
				})
				m.rebuildViewport()
				return m, m.waitForEvent()
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
			// Double ctrl+c to quit — first press shows warning, second quits
			if m.ctrlCPending {
				// Second press — actually quit
				if m.chimePlayer != nil {
					m.chimePlayer.Stop()
				}
				return m, tea.Quit
			}
			// First press — show warning, arm the timeout
			m.ctrlCPending = true
			m.notify.Push(Notification{Type: NotifyWarn, Text: "Press ctrl+c again to quit", Duration: 3 * time.Second})
			return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return ctrlCTimeoutMsg{} })

		// Permission picker — arrow keys to select, enter to confirm
		case "left", "h":
			if m.state == chatPermission {
				if m.permChoice > 0 {
					m.permChoice--
				}
				return m, nil
			}
		case "right", "l":
			if m.state == chatPermission {
				if m.permChoice < 3 {
					m.permChoice++
				}
				return m, nil
			}

		// Permission single-key shortcuts — respond instantly without enter
		case "y":
			if m.state == chatPermission {
				m.input.SetValue("")
				return m.handlePermissionResponse("y"), m.waitForEvent()
			}
		case "n":
			if m.state == chatPermission {
				m.input.SetValue("")
				return m.handlePermissionResponse("n"), m.waitForEvent()
			}
		case "a":
			if m.state == chatPermission {
				m.input.SetValue("")
				return m.handlePermissionResponse("a"), m.waitForEvent()
			}

		case "enter":
			// Any input resets the ctrl+c quit guard
			m.ctrlCPending = false

			// Permission: enter confirms the picker selection
			if m.state == chatPermission {
				m.input.SetValue("")
				choices := []string{"y", "n", "a", "all"}
				return m.handlePermissionResponse(choices[m.permChoice]), m.waitForEvent()
			}

			prompt := strings.TrimSpace(m.input.Value())
			m.input.SetValue("")

			if prompt == "" {
				return m, nil
			}

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
				m.lastNarrationText = msg.text
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

	case ctrlCTimeoutMsg:
		// ctrl+c window expired — reset the guard
		m.ctrlCPending = false
		return m, nil

	case narrateTickMsg:
		if m.state == chatStreaming && len(m.recentActions) > 0 &&
			time.Since(m.lastNarration) > 6*time.Second {
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
		// Cycle spinner color through accent palette
		m.spinner.Style = lipgloss.NewStyle().Foreground(spinnerColors[m.notify.frame%len(spinnerColors)])
		// Re-render for spinner during active states
		if m.state == chatStreaming || m.state == chatPermission {
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
	h := m.height - 4 // header + topSep + bottomSep + input
	if h < 5 {
		h = 5
	}
	w := m.width
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
	// Dynamic elements that reduce viewport space — must match compose logic exactly
	taskH := m.taskPanelHeight()
	activeToolH := 0
	if m.state == chatStreaming {
		activeToolH = 1
	}
	// notifyBar is only shown when activeToolLine is NOT showing
	notifyH := 0
	if activeToolH == 0 {
		notifyH = len(m.notify.active)
	}
	pickerH := 0
	if m.state == chatPermission {
		pickerH = 5 // context box (3) + options (1) + description (1)
	}
	suggestH := 0
	if len(m.suggestions) > 0 && m.state == chatIdle {
		suggestH = 1
	}
	// Fixed: header(1) + topSep(1) + bottomSep(1) + input(1) = 4
	targetH := m.height - 4 - notifyH - taskH - pickerH - suggestH - activeToolH
	if targetH < 5 {
		targetH = 5
	}
	if m.viewport.Height != targetH {
		m.viewport.Height = targetH
	}

	var sb strings.Builder
	contentW := m.width - 4

	for _, seg := range m.segments {
		switch seg.kind {
		case "tool":
			block := renderToolBlock(seg.tool.name, seg.tool.args, seg.tool.output, seg.tool.state, contentW, m.config.WorkDir)
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
		m.agent = NewAgent(client, m.config.WorkDir, m.eventCh, m.stopCh, m.tasks, m.glue)

		// Only restore session if --resume flag was passed
		if m.config.Resume {
			if session := LoadSession(m.config.WorkDir, m.config.Model); session != nil {
				m.agent.history = session.History
				m.tokens = session.Tokens
				if session.Title != "" {
					m.title = session.Title
				}
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

	m.agentDone = make(chan struct{})
	go func() {
		m.agent.Run(prompt)
		close(m.eventCh)
		close(m.agentDone)
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
			tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return narrateTickMsg{} }),
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
			tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return narrateTickMsg{} }),
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
			tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return narrateTickMsg{} }),
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
	if m.state != chatStreaming && m.state != chatPermission && evt.Type != EventDone {
		return nil
	}

	switch evt.Type {
	case EventTextDelta:
		// Filter out <think>...</think> reasoning blocks (MiniMax, DeepSeek, etc.)
		text := evt.Text
		for text != "" {
			if m.inThink {
				if end := strings.Index(text, "</think>"); end >= 0 {
					m.inThink = false
					text = text[end+len("</think>"):]
					continue
				}
				// Still inside think block — discard everything
				text = ""
			} else {
				if start := strings.Index(text, "<think>"); start >= 0 {
					// Write text before the tag
					if start > 0 {
						m.streaming.WriteString(text[:start])
					}
					m.inThink = true
					if !m.notify.HasActive() {
						m.notify.Push(Notification{Type: NotifyProgress, Text: "Thinking..."})
					}
					text = text[start+len("<think>"):]
					continue
				}
				// No think tags — write as-is
				m.streaming.WriteString(text)
				text = ""
			}
		}
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
			dividerText := m.lastNarrationText
			// If no narration yet, generate one now from recent actions
			if dividerText == "" && len(m.recentActions) > 0 {
				dividerText = m.glue.Narrate(m.recentActions)
				if dividerText != "" {
					m.lastNarrationText = dividerText
				}
			}
			if dividerText == "" {
				dividerText = fmt.Sprintf("turn %d", evt.Turn)
			}
			m.segments = append(m.segments, segment{
				kind: "divider",
				text: "\n  " + renderGradientText(dividerText, activeTheme.Accent, activeTheme.Cyan, activeTheme.Purple) + "\n\n",
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

		// Trigger narration every 3 tool completions for responsive feedback
		if m.toolsDone%3 == 0 && len(m.recentActions) > 0 {
			m.lastNarration = time.Now()
			actions := make([]string, len(m.recentActions))
			copy(actions, m.recentActions)
			glue := m.glue
			return tea.Batch(m.waitForEvent(), func() tea.Msg {
				narration := glue.Narrate(actions)
				return glueResultMsg{kind: "narrate", text: narration}
			})
		}
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
			SaveSession(m.agent, m.tokens, m.title)
		}
		m.state = chatDoneFlash
		m.flashFrames = 6
		m.rebuildViewport()
		m.chimePlayer = PlayChimeAsync()

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

	case EventPermission:
		m.flushStreamingText()
		m.state = chatPermission
		m.permRequest = evt.Permission
		m.permChoice = 0 // default to "Allow"
		m.input.Placeholder = ""
		m.permCount++
		if m.permCount == 1 {
			m.notify.Push(Notification{Type: NotifyInfo, Text: "Tip: /trust all to auto-approve this session", Duration: 8 * time.Second})
		}
		m.rebuildViewport()
		return m.waitForEvent() // keep listening for other events while waiting

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
	m.inThink = false
	text := m.streaming.String()
	// Strip any residual think tags
	text = stripThinkTags(text)
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
		tokenStr = styleMuted.Render(fmt.Sprintf("%.1fk tok", float64(totalTokens)/1000))
	} else {
		tokenStr = styleMuted.Render(fmt.Sprintf("%d tok", totalTokens))
	}
	dot := styleDim.Render(" · ")
	statusParts := []string{modelStr, tokenStr}
	if m.files > 0 {
		statusParts = append(statusParts, styleMuted.Render(fmt.Sprintf("%d files", m.files)))
	}
	switch m.state {
	case chatStreaming:
		statusParts = append(statusParts, styleMuted.Render("streaming"))
	case chatPermission:
		statusParts = append(statusParts, styleWarn.Render("⚡ permission"))
	case chatPlanning:
		statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colPurple).Render("◆ planning"))
	case chatPlanReview:
		statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colPurple).Render("◆ review"))
	case chatDoneFlash:
		statusParts = append(statusParts, styleOK.Render("✓ done"))
	}
	statusRight := strings.Join(statusParts, dot)

	titleLeft := styleAccentText.Render(" codebase")
	if m.title != "" {
		titleLeft += styleDim.Render(" · ") + styleMuted.Render(m.title)
	}

	gap := m.width - lipgloss.Width(titleLeft) - lipgloss.Width(statusRight) - 2
	if gap < 1 && m.title != "" {
		maxTitle := m.width - lipgloss.Width(styleAccentText.Render(" codebase")) - lipgloss.Width(statusRight) - lipgloss.Width(styleDim.Render(" · ")) - 6
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
		gap = m.width - lipgloss.Width(titleLeft) - lipgloss.Width(statusRight) - 2
	}
	if gap < 1 {
		gap = 1
	}
	header := titleLeft + strings.Repeat(" ", gap) + statusRight

	// ── Separator color (state feedback) ─────────────────────
	var sepColor lipgloss.Color
	switch m.state {
	case chatStreaming:
		sepColor = colBorderHi
	case chatPermission:
		sepColor = colOrange
	case chatDoneFlash:
		colorIdx := len(flashCycleColors) - m.flashFrames
		if colorIdx < 0 {
			colorIdx = 0
		}
		if colorIdx >= len(flashCycleColors) {
			colorIdx = len(flashCycleColors) - 1
		}
		sepColor = flashCycleColors[colorIdx]
	case chatPlanning, chatPlanReview:
		sepColor = colPurple
	default:
		sepColor = colBorder
	}
	sepStyle := lipgloss.NewStyle().Foreground(sepColor)
	topSep := sepStyle.Render(strings.Repeat("─", m.width))

	// Bottom separator — embed scroll % when user scrolled up
	var bottomSep string
	if m.userScrolled && m.viewport.TotalLineCount() > m.viewport.Height {
		pct := 0
		if m.viewport.TotalLineCount()-m.viewport.Height > 0 {
			pct = int(float64(m.viewport.YOffset) / float64(m.viewport.TotalLineCount()-m.viewport.Height) * 100)
		}
		label := fmt.Sprintf(" ↑ %d%% ", pct)
		labelW := lipgloss.Width(label)
		leftLen := (m.width - labelW) / 2
		rightLen := m.width - leftLen - labelW
		if leftLen < 0 {
			leftLen = 0
		}
		if rightLen < 0 {
			rightLen = 0
		}
		bottomSep = sepStyle.Render(strings.Repeat("─", leftLen)) +
			styleDim.Render(label) +
			sepStyle.Render(strings.Repeat("─", rightLen))
	} else {
		bottomSep = topSep
	}

	// ── Task panel + notifications (above viewport) ──────────
	taskPanel := m.renderTaskPanel()
	notifyBar := m.notify.Render(m.width)

	// ── Activity status (pinned above bottom sep) ───────────
	activeToolLine := ""
	if m.state == chatStreaming {
		activeToolLine = m.renderActiveToolLine()
	}

	// ── Permission picker ────────────────────────────────────
	permPicker := ""
	if m.state == chatPermission {
		permPicker = m.renderPermissionPicker()
	}

	// ── Suggestions ──────────────────────────────────────────
	suggestBar := ""
	if len(m.suggestions) > 0 && m.state == chatIdle {
		suggestBar = renderSuggestions(m.suggestions, m.width)
	}

	// ── Viewport ─────────────────────────────────────────────
	body := m.viewport.View()

	// ── Input ────────────────────────────────────────────────
	inputLine := OSC633PromptStart() + " " + m.input.View() + OSC633PromptEnd()
	var hint string
	switch m.state {
	case chatStreaming:
		hint = styleDim.Render("ctrl+c stop") + dot + styleDim.Render("↑↓ scroll")
	case chatPermission:
		hint = styleDim.Render("←→ select") + dot + styleDim.Render("enter confirm") + dot + styleDim.Render("y/n/a")
	case chatPlanning:
		hint = styleDim.Render("ctrl+c cancel") + dot + styleDim.Render("enter answer")
	case chatPlanReview:
		hint = styleDim.Render("ctrl+c cancel") + dot + styleDim.Render("\"go\" approve") + dot + styleDim.Render("\"skip\" skip")
	default:
		hint = styleDim.Render("ctrl+c quit") + dot + styleDim.Render("/help") + dot + styleDim.Render("↑↓ scroll")
	}
	inputGap := m.width - lipgloss.Width(inputLine) - lipgloss.Width(hint) - 2
	if inputGap < 1 {
		inputGap = 1
	}
	inputRow := inputLine + strings.Repeat(" ", inputGap) + hint

	// ── Compose ──────────────────────────────────────────────
	var out strings.Builder
	out.WriteString(header + "\n")
	out.WriteString(topSep + "\n")
	out.WriteString(body + "\n")
	if taskPanel != "" {
		out.WriteString(taskPanel + "\n")
	}
	if notifyBar != "" && activeToolLine == "" {
		out.WriteString(notifyBar) // already includes trailing \n per line
	}
	if activeToolLine != "" {
		out.WriteString(activeToolLine + "\n")
	}
	out.WriteString(bottomSep + "\n")
	if permPicker != "" {
		out.WriteString(permPicker + "\n")
	}
	if suggestBar != "" {
		out.WriteString(suggestBar + "\n")
	}
	out.WriteString(inputRow)

	return out.String()
}

// ── Permission picker ─────────────────────────────────────────

// renderPermissionPicker builds the permission context box + selector, pinned above input.
func (m *chatModel) renderPermissionPicker() string {
	var sb strings.Builder

	if m.permRequest != nil {
		// Build box content — use lipgloss.PlaceHorizontal for reliable centering.
		// Key fix: set an explicit width on the box so borders align correctly.
		// Don't mix ANSI styles inside the box content string — style the box itself.

		// Line 1: action summary with risk badge
		line1 := m.permRequest.Summary
		riskTag := ""
		if m.permRequest.Risk != "" {
			riskTag = " [" + m.permRequest.Risk + " RISK]"
		}

		// Line 2: explanation (if available)
		line2 := ""
		if m.permRequest.Explanation != "" {
			line2 = m.permRequest.Explanation
		}

		// Calculate box width — clamp to terminal width with margin
		maxBoxW := m.width - 4
		if maxBoxW < 30 {
			maxBoxW = 30
		}
		contentW := lipgloss.Width(line1 + riskTag)
		if line2 != "" {
			if w := lipgloss.Width(line2); w > contentW {
				contentW = w
			}
		}
		boxInnerW := contentW + 4 // padding
		if boxInnerW > maxBoxW {
			boxInnerW = maxBoxW
		}

		// Build styled content lines
		var boxLines []string

		// Action line with risk coloring
		actionLine := styleMuted.Render(line1)
		if riskTag != "" {
			switch m.permRequest.Risk {
			case "LOW":
				actionLine += styleOK.Render(riskTag)
			case "MEDIUM":
				actionLine += styleWarn.Render(riskTag)
			case "HIGH":
				actionLine += lipgloss.NewStyle().Foreground(colError).Bold(true).Render(riskTag)
			}
		}
		boxLines = append(boxLines, actionLine)

		if line2 != "" {
			boxLines = append(boxLines, styleDim.Render(line2))
		}

		boxContent := strings.Join(boxLines, "\n")

		// Render box with explicit width for reliable borders
		box := lipgloss.NewStyle().
			Width(boxInnerW).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colOrange).
			Padding(0, 1).
			Render(boxContent)

		// Center the box
		sb.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, box))
		sb.WriteString("\n")
	}

	// Option picker — also use PlaceHorizontal for centering
	type permOption struct {
		label string
		desc  string
	}
	options := []permOption{
		{"Allow", "Allow this action once"},
		{"Deny", "Block this action"},
		{"Always", "Auto-approve this tool for the session"},
		{"Trust all", "Auto-approve all tools for the session"},
	}

	var optParts []string
	for i, opt := range options {
		if i == m.permChoice {
			optParts = append(optParts, lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("> "+opt.label))
		} else {
			optParts = append(optParts, styleDim.Render("  "+opt.label))
		}
	}
	optLine := strings.Join(optParts, "   ")
	sb.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, optLine))
	sb.WriteString("\n")

	descText := styleMuted.Render(options[m.permChoice].desc)
	sb.WriteString(lipgloss.PlaceHorizontal(m.width, lipgloss.Center, descText))

	return sb.String()
}

// ── Active tool status line ──────────────────────────────────

// renderActiveToolLine builds a compact one-line status pinned above the bottom separator.
func (m *chatModel) renderActiveToolLine() string {
	// Find last pending tool — show tool name + detail
	for i := len(m.segments) - 1; i >= 0; i-- {
		if m.segments[i].kind == "tool" && m.segments[i].tool.state == "pending" {
			t := m.segments[i].tool
			detail := ""
			if t.args != nil {
				if p, ok := t.args["path"].(string); ok {
					detail = p
				} else if p, ok := t.args["command"].(string); ok {
					if len(p) > 60 {
						p = p[:57] + "..."
					}
					detail = p
				} else if p, ok := t.args["pattern"].(string); ok {
					detail = p
				}
			}
			line := " " + m.spinner.View() + " " + styleToolName.Render(t.name)
			if detail != "" {
				line += " " + styleFilePath.Render(detail)
			}
			return line
		}
	}
	// No pending tool — show current activity (thinking, narration, task status)
	activity := m.currentToolActivity()
	return " " + m.spinner.View() + " " + styleMuted.Render(activity)
}

// stripThinkTags removes <think>...</think> blocks and stray tags from text.
func stripThinkTags(s string) string {
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "</think>")
		if end < 0 {
			// Unclosed tag — strip from <think> to end
			s = s[:start]
			break
		}
		s = s[:start] + s[start+end+len("</think>"):]
	}
	return strings.TrimSpace(s)
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

// ── Permission response handler ──────────────────────────────

func (m *chatModel) handlePermissionResponse(input string) chatModel {
	resp := parsePermissionInput(input)
	if m.agent != nil {
		select {
		case m.agent.permCh <- resp:
		default:
			// Channel full or closed — don't block
		}
	}
	decision := styleOK.Render("  ✓ allowed")
	if !resp.Allowed {
		decision = styleWarn.Render("  ✗ denied")
	} else if resp.TrustLevel == PermTrustTool {
		decision = styleOK.Render("  ✓ always allow " + m.permRequest.Tool)
	} else if resp.TrustLevel == PermTrustAll {
		decision = styleOK.Render("  ✓ trusted all")
	}
	m.segments = append(m.segments, segment{
		kind: "text",
		text: decision + "\n",
	})
	m.permRequest = nil
	m.state = chatStreaming
	m.input.Placeholder = "describe what you want to build..."
	m.rebuildViewport()
	return *m
}

// ── Task panel rendering ─────────────────────────────────────

// taskPanelHeight returns how many lines the task panel takes.
func (m *chatModel) taskPanelHeight() int {
	if m.tasks == nil || m.tasks.Count() == 0 {
		return 0
	}
	return m.tasks.Count() // one line per task
}

// renderTaskPanel builds a compact task checklist pinned near the bottom.
func (m *chatModel) renderTaskPanel() string {
	if m.tasks == nil || m.tasks.Count() == 0 {
		return ""
	}
	tasks := m.tasks.List()

	var sb strings.Builder
	for _, t := range tasks {
		var icon string
		var textStyle lipgloss.Style
		switch t.Status {
		case TaskCompleted:
			icon = styleOK.Render(" ✓")
			textStyle = lipgloss.NewStyle().Foreground(colDim).Strikethrough(true)
		case TaskInProgress:
			icon = " " + m.spinner.View()
			textStyle = lipgloss.NewStyle().Foreground(colText)
		default:
			if m.tasks.IsBlocked(t) {
				icon = styleDim.Render(" ○")
				textStyle = styleDim
			} else {
				icon = styleDim.Render(" ○")
				textStyle = styleMuted
			}
		}
		subject := t.Subject
		maxSubject := m.width - 8
		if maxSubject < 20 {
			maxSubject = 20
		}
		runes := []rune(subject)
		if len(runes) > maxSubject {
			subject = string(runes[:maxSubject-3]) + "..."
		}
		sb.WriteString(icon + " " + textStyle.Render(subject) + "\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

// currentToolActivity returns human-readable text for the current tool action.
func (m *chatModel) currentToolActivity() string {
	// Check pending tools first
	for i := len(m.segments) - 1; i >= 0; i-- {
		if m.segments[i].kind == "tool" && m.segments[i].tool.state == "pending" {
			return toolActivityText(m.segments[i].tool.name, m.segments[i].tool.args)
		}
	}
	// Show active task's activeForm if available
	if m.tasks != nil {
		if active := m.tasks.ActiveTask(); active != nil && active.ActiveForm != "" {
			return active.ActiveForm
		}
	}
	if m.lastNarrationText != "" {
		return m.lastNarrationText
	}
	return "thinking..."
}

// toolActivityText converts a tool name + args into natural language.
func toolActivityText(name string, args map[string]any) string {
	switch name {
	case "read_file":
		if p, ok := args["path"].(string); ok {
			parts := strings.Split(p, "/")
			return "reading " + parts[len(parts)-1]
		}
		return "reading file..."
	case "write_file":
		if p, ok := args["path"].(string); ok {
			parts := strings.Split(p, "/")
			return "writing " + parts[len(parts)-1]
		}
		return "writing file..."
	case "edit_file", "multi_edit":
		if p, ok := args["path"].(string); ok {
			parts := strings.Split(p, "/")
			return "editing " + parts[len(parts)-1]
		}
		return "editing..."
	case "search_files":
		if p, ok := args["pattern"].(string); ok {
			return "searching for " + p
		}
		return "searching..."
	case "list_files":
		return "scanning files..."
	case "shell":
		if c, ok := args["command"].(string); ok {
			fields := strings.Fields(c)
			if len(fields) > 0 {
				return "running " + fields[0]
			}
		}
		return "running command..."
	case "dispatch_agent":
		return "researching..."
	case "web_search":
		if q, ok := args["query"].(string); ok {
			if len(q) > 30 {
				q = q[:27] + "..."
			}
			return "searching web: " + q
		}
		return "searching web..."
	case "create_task":
		if s, ok := args["subject"].(string); ok {
			return "planning: " + s
		}
		return "planning..."
	case "update_task":
		return "updating task..."
	case "list_tasks":
		return "checking tasks..."
	case "get_task":
		return "reviewing task..."
	case "git_status":
		return "checking git status..."
	case "git_diff":
		return "viewing diff..."
	case "git_log":
		return "checking git history..."
	case "git_commit":
		if m, ok := args["message"].(string); ok {
			if len(m) > 30 {
				m = m[:27] + "..."
			}
			return "committing: " + m
		}
		return "committing..."
	case "git_branch":
		if n, ok := args["name"].(string); ok && n != "" {
			return "switching to " + n
		}
		return "listing branches..."
	default:
		return name + "..."
	}
}

