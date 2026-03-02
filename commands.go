package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ──────────────────────────────────────────────────────────────
//  Slash commands — intercept "/" prefixed input
// ──────────────────────────────────────────────────────────────

type slashCommand struct {
	name    string
	aliases []string
	desc    string
	handler func(m *chatModel, args string) tea.Cmd
}

var commands []slashCommand

func init() {
	commands = []slashCommand{
	{
		name: "help",
		desc: "Show available commands",
		handler: cmdHelp,
	},
	{
		name: "clear",
		desc: "Clear conversation display",
		handler: func(m *chatModel, args string) tea.Cmd {
			m.segments = []segment{
				{kind: "text", text: styleMuted.Render("  Conversation cleared. History preserved — use /compact to reduce context.\n")},
			}
			m.suggestions = nil
			m.rebuildViewport()
			return nil
		},
	},
	{
		name: "compact",
		desc: "Compact conversation history to save context",
		handler: func(m *chatModel, args string) tea.Cmd {
			if m.agent == nil || len(m.agent.history) < 4 {
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleMuted.Render("  Nothing to compact — conversation is short.\n"),
				})
				m.rebuildViewport()
				return nil
			}
			compacted, ok := compactHistory(m.agent.client, m.agent.history)
			if ok {
				before := len(m.agent.history)
				m.agent.history = compacted
				after := len(m.agent.history)
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleOK.Render("  ✓ ") + styleMuted.Render(fmt.Sprintf("Compacted %d → %d messages.\n", before, after)),
				})
			} else {
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleMuted.Render("  Conversation is already compact.\n"),
				})
			}
			m.rebuildViewport()
			return nil
		},
	},
	{
		name: "model",
		desc: "Show or switch model (usage: /model [name])",
		handler: func(m *chatModel, args string) tea.Cmd {
			if args == "" {
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleMuted.Render("  Current model: ") + styleAccentText.Render(m.config.Model) + "\n" +
						styleMuted.Render("  Usage: /model <name> to switch\n"),
				})
			} else {
				old := m.config.Model
				m.config.Model = args
				// Update the agent's client if it exists
				if m.agent != nil {
					m.agent.client = NewLLMClient(m.config.APIKey, m.config.BaseURL, m.config.Model)
				}
				// Update glue clients to use new model as fallback
				if m.glue != nil {
					m.glue = NewGlueClient(m.config)
				}
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleOK.Render("  ✓ ") + styleMuted.Render(fmt.Sprintf("Switched model: %s → %s\n", old, args)),
				})
			}
			m.rebuildViewport()
			return nil
		},
	},
	{
		name:    "session",
		aliases: []string{"info"},
		desc:    "Show session info",
		handler: func(m *chatModel, args string) tea.Cmd {
			totalTokens := m.tokens.PromptTokens + m.tokens.CompletionTokens
			histLen := 0
			if m.agent != nil {
				histLen = len(m.agent.history)
			}
			var sb strings.Builder
			sb.WriteString(styleMuted.Render("  Session info:") + "\n")
			sb.WriteString(styleDim.Render("  Model:    ") + styleMuted.Render(m.config.Model) + "\n")
			// Protocol (API format)
			proto := ProtocolOpenAI
			if m.agent != nil {
				proto = m.agent.client.Protocol
			}
			protoLabel := "Chat Completions (OpenAI)"
			if proto == ProtocolAnthropic {
				protoLabel = "Messages API (Anthropic)"
			}
			sb.WriteString(styleDim.Render("  Protocol: ") + styleMuted.Render(protoLabel) + "\n")
			// Token counts with cost estimate
			tokenLine := fmt.Sprintf("%d (%d in + %d out)", totalTokens, m.tokens.PromptTokens, m.tokens.CompletionTokens)
			cost := estimateCost(m.config.Model, m.tokens.PromptTokens, m.tokens.CompletionTokens)
			if cost != "" {
				tokenLine += " " + styleDim.Render("~"+cost)
			}
			sb.WriteString(styleDim.Render("  Tokens:   ") + styleMuted.Render(tokenLine) + "\n")
			sb.WriteString(styleDim.Render("  Files:    ") + styleMuted.Render(fmt.Sprintf("%d modified", m.files)) + "\n")
			sb.WriteString(styleDim.Render("  Turns:    ") + styleMuted.Render(fmt.Sprintf("%d", m.turns)) + "\n")
			sb.WriteString(styleDim.Render("  History:  ") + styleMuted.Render(fmt.Sprintf("%d messages", histLen)) + "\n")
			sb.WriteString(styleDim.Render("  WorkDir:  ") + styleMuted.Render(m.config.WorkDir) + "\n")
			// Task stats
			if m.tasks != nil && m.tasks.Count() > 0 {
				p, ip, c := m.tasks.Stats()
				sb.WriteString(styleDim.Render("  Tasks:    ") + styleMuted.Render(fmt.Sprintf("%d done, %d active, %d pending", c, ip, p)) + "\n")
			}
			if m.title != "" {
				sb.WriteString(styleDim.Render("  Title:    ") + styleMuted.Render(m.title) + "\n")
			}
			// Trust level
			trustLevel := "ask"
			if m.agent != nil && m.agent.permState.Level == PermTrustAll {
				trustLevel = "all (auto-approve)"
			}
			sb.WriteString(styleDim.Render("  Trust:    ") + styleMuted.Render(trustLevel) + "\n")
			m.segments = append(m.segments, segment{kind: "text", text: sb.String()})
			m.rebuildViewport()
			return nil
		},
	},
	{
		name: "copy",
		desc: "Copy last assistant response to clipboard",
		handler: func(m *chatModel, args string) tea.Cmd {
			// Find last text segment (skip user/tool/divider/error)
			var lastText string
			for i := len(m.segments) - 1; i >= 0; i-- {
				seg := m.segments[i]
				if seg.kind == "text" && !strings.Contains(seg.text, "❯ ") {
					lastText = seg.text
					break
				}
			}
			if lastText == "" {
				m.notify.Push(Notification{Type: NotifyWarn, Text: "Nothing to copy"})
				return nil
			}
			// Strip ANSI escape codes
			clean := stripANSI(lastText)
			clean = strings.TrimSpace(clean)
			if err := copyToClipboard(clean); err != nil {
				m.notify.Push(Notification{Type: NotifyWarn, Text: "No clipboard tool found (install xclip)"})
			} else {
				m.notify.Push(Notification{Type: NotifySuccess, Text: "Copied to clipboard"})
			}
			return nil
		},
	},
	{
		name:    "tasks",
		aliases: []string{"todo"},
		desc:    "Show task list",
		handler: func(m *chatModel, args string) tea.Cmd {
			if m.tasks == nil || m.tasks.Count() == 0 {
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleMuted.Render("  No tasks yet — the agent creates tasks automatically for multi-step work.\n"),
				})
				m.rebuildViewport()
				return nil
			}

			tasks := m.tasks.List()
			pending, inProgress, completed := m.tasks.Stats()
			total := pending + inProgress + completed

			var sb strings.Builder
			sb.WriteString(styleMuted.Render(fmt.Sprintf("  Tasks: %d/%d complete", completed, total)) + "\n\n")

			for _, t := range tasks {
				var icon, status string
				switch t.Status {
				case TaskCompleted:
					icon = styleOK.Render("  ✓")
					status = styleDim.Render(" done")
				case TaskInProgress:
					icon = styleAccentText.Render("  ◐")
					status = styleAccentText.Render(" working")
				default:
					if m.tasks.IsBlocked(t) {
						icon = styleDim.Render("  ⊘")
						status = styleDim.Render(" blocked")
					} else {
						icon = styleDim.Render("  ○")
						status = ""
					}
				}
				sb.WriteString(icon + " " + styleMuted.Render(t.Subject) + status + "\n")
			}
			m.segments = append(m.segments, segment{kind: "text", text: sb.String()})
			m.rebuildViewport()
			return nil
		},
	},
	{
		name: "theme",
		desc: "Switch color theme (usage: /theme dark|light|retro)",
		handler: func(m *chatModel, args string) tea.Cmd {
			if args == "" {
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleMuted.Render("  Current theme: ") + styleAccentText.Render(activeTheme.Name) + "\n" +
						styleMuted.Render("  Usage: /theme dark|light|retro\n"),
				})
				m.rebuildViewport()
				return nil
			}
			switch strings.ToLower(args) {
			case "dark", "light", "retro":
				setTheme(args)
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleOK.Render("  ✓ ") + styleMuted.Render("Switched to "+activeTheme.Name+" theme.\n"),
				})
				m.rebuildViewport()
			default:
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleWarn.Render("  Unknown theme: "+args) + styleMuted.Render(". Try: dark, light, retro\n"),
				})
				m.rebuildViewport()
			}
			return nil
		},
	},
	{
		name:    "diagnostics",
		aliases: []string{"diag"},
		desc:    "Toggle language diagnostics (usage: /diagnostics on|off)",
		handler: func(m *chatModel, args string) tea.Cmd {
			if m.agent == nil {
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleMuted.Render("  No active agent — diagnostics start with your first task.\n"),
				})
				m.rebuildViewport()
				return nil
			}
			switch strings.ToLower(args) {
			case "on":
				m.agent.diag.Enabled = true
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleOK.Render("  ✓ ") + styleMuted.Render("Diagnostics enabled.\n"),
				})
			case "off":
				m.agent.diag.Enabled = false
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleOK.Render("  ✓ ") + styleMuted.Render("Diagnostics disabled.\n"),
				})
			default:
				status := "off"
				if m.agent.diag.Enabled {
					status = "on"
				}
				checkers := m.agent.diag.DetectedCheckers()
				var sb strings.Builder
				sb.WriteString(styleMuted.Render("  Diagnostics: ") + styleAccentText.Render(status) + "\n")
				if len(checkers) > 0 {
					sb.WriteString(styleMuted.Render("  Detected: ") + styleDim.Render(strings.Join(checkers, ", ")) + "\n")
				} else {
					sb.WriteString(styleDim.Render("  No language checkers detected for this project.\n"))
				}
				sb.WriteString(styleMuted.Render("  Usage: /diagnostics on | /diagnostics off\n"))
				m.segments = append(m.segments, segment{kind: "text", text: sb.String()})
			}
			m.rebuildViewport()
			return nil
		},
	},
	{
		name: "trust",
		desc: "Set permission level (usage: /trust ask|all|reset)",
		handler: func(m *chatModel, args string) tea.Cmd {
			if m.agent == nil {
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleMuted.Render("  No active agent — permission state is set when you start a task.\n"),
				})
				m.rebuildViewport()
				return nil
			}
			switch strings.ToLower(args) {
			case "all":
				m.agent.permState.Level = PermTrustAll
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleOK.Render("  ✓ ") + styleMuted.Render("Trust all — auto-approving all tools this session.\n"),
				})
			case "ask", "reset":
				m.agent.permState.Level = PermAsk
				m.agent.permState.TrustedTools = map[string]bool{}
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleOK.Render("  ✓ ") + styleMuted.Render("Reset — will ask before each mutation.\n"),
				})
			default:
				current := "ask (prompt before mutations)"
				if m.agent.permState.Level == PermTrustAll {
					current = "all (auto-approve everything)"
				}
				trusted := []string{}
				for t := range m.agent.permState.TrustedTools {
					trusted = append(trusted, t)
				}
				var sb strings.Builder
				sb.WriteString(styleMuted.Render("  Permission level: ") + styleAccentText.Render(current) + "\n")
				if len(trusted) > 0 {
					sb.WriteString(styleMuted.Render("  Trusted tools: ") + styleDim.Render(strings.Join(trusted, ", ")) + "\n")
				}
				sb.WriteString(styleMuted.Render("  Usage: /trust all | /trust ask | /trust reset\n"))
				m.segments = append(m.segments, segment{kind: "text", text: sb.String()})
			}
			m.rebuildViewport()
			return nil
		},
	},
	{
		name: "diff",
		desc: "Open file diff in VS Code/Cursor (usage: /diff <file>)",
		handler: func(m *chatModel, args string) tea.Cmd {
			if !termInfo.IsVSCode && !termInfo.IsCursor {
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleWarn.Render("  /diff requires VS Code or Cursor terminal.\n"),
				})
				m.rebuildViewport()
				return nil
			}
			if args == "" {
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleMuted.Render("  Usage: /diff <file-path>\n"),
				})
				m.rebuildViewport()
				return nil
			}
			// Open git diff for the file in VS Code
			cmd := exec.Command("code", "--diff", args, args)
			cmd.Start()
			m.notify.Push(Notification{Type: NotifyInfo, Text: "Opening diff in editor..."})
			return nil
		},
	},
	{
		name: "setup",
		desc: "Re-run the setup wizard",
		handler: func(m *chatModel, args string) tea.Cmd {
			return func() tea.Msg {
				return enterSetupMsg{}
			}
		},
	},
	{
		name:    "quit",
		aliases: []string{"exit", "q"},
		desc:    "Exit codebase",
		handler: func(m *chatModel, args string) tea.Cmd {
			return tea.Quit
		},
	},
	}
}

// handleCommand tries to match and execute a slash command.
// Returns (cmd, true) if a command was handled, (nil, false) otherwise.
func (m *chatModel) handleCommand(input string) (tea.Cmd, bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "/") {
		return nil, false
	}

	parts := strings.SplitN(input[1:], " ", 2)
	name := strings.ToLower(parts[0])
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	for _, cmd := range commands {
		if cmd.name == name {
			return cmd.handler(m, args), true
		}
		for _, alias := range cmd.aliases {
			if alias == name {
				return cmd.handler(m, args), true
			}
		}
	}

	// Unknown command
	m.segments = append(m.segments, segment{
		kind: "text",
		text: styleWarn.Render("  Unknown command: /"+name) + styleMuted.Render(". Type /help for available commands.\n"),
	})
	m.rebuildViewport()
	return nil, true
}

func cmdHelp(m *chatModel, args string) tea.Cmd {
	var sb strings.Builder
	sb.WriteString(styleMuted.Render("  Available commands:") + "\n\n")
	for _, cmd := range commands {
		name := styleAccentText.Render("  /" + cmd.name)
		desc := styleMuted.Render(" — " + cmd.desc)
		sb.WriteString(name + desc + "\n")
	}
	sb.WriteString("\n" + styleMuted.Render("  Key shortcuts:") + "\n")
	sb.WriteString(styleDim.Render("  ctrl+c") + styleMuted.Render(" quit/stop/cancel") + "\n")
	sb.WriteString(styleDim.Render("  enter") + styleMuted.Render("  send message") + "\n")
	sb.WriteString(styleDim.Render("  ↑↓/pgup/pgdn") + styleMuted.Render(" scroll") + "\n")
	m.segments = append(m.segments, segment{kind: "text", text: sb.String()})
	m.rebuildViewport()
	return nil
}

// ──────────────────────────────────────────────────────────────
//  Cost estimation (rough per-1M-token rates)
// ──────────────────────────────────────────────────────────────

func estimateCost(model string, promptTokens, completionTokens int) string {
	// Approximate costs per million tokens (input, output)
	type pricing struct{ input, output float64 }
	rates := map[string]pricing{
		"gpt-4o":        {2.50, 10.0},
		"gpt-4o-mini":   {0.15, 0.60},
		"gpt-4-turbo":   {10.0, 30.0},
		"gpt-4.1":       {2.00, 8.0},
		"gpt-4.1-mini":  {0.40, 1.60},
		"gpt-4.1-nano":  {0.10, 0.40},
		"o3-mini":       {1.10, 4.40},
		"claude-sonnet": {3.00, 15.0},
		"claude-opus":   {15.0, 75.0},
		"claude-haiku":  {0.25, 1.25},
		"minimax-m2.5":  {1.00, 5.00},
		"minimax-m2.1":  {0.50, 2.50},
		"minimax-m2":    {0.50, 2.50},
		"glm-4":         {0.14, 0.14},
		"glm-4.7":       {0.14, 0.14},
	}

	// Try exact match, then prefix match
	modelLower := strings.ToLower(model)
	var rate pricing
	found := false
	if r, ok := rates[modelLower]; ok {
		rate = r
		found = true
	}
	if !found {
		for prefix, r := range rates {
			if strings.HasPrefix(modelLower, prefix) {
				rate = r
				found = true
				break
			}
		}
	}
	if !found {
		return ""
	}

	cost := (float64(promptTokens)/1_000_000)*rate.input + (float64(completionTokens)/1_000_000)*rate.output
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}

// ──────────────────────────────────────────────────────────────
//  Clipboard helpers
// ──────────────────────────────────────────────────────────────

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

func copyToClipboard(text string) error {
	// Try clipboard tools in order
	tools := []struct {
		name string
		args []string
	}{
		{"xclip", []string{"-selection", "clipboard"}},
		{"xsel", []string{"--clipboard", "--input"}},
		{"pbcopy", nil},     // macOS
		{"clip.exe", nil},   // WSL
	}

	for _, tool := range tools {
		path, err := exec.LookPath(tool.name)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, tool.args...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no clipboard tool found")
}
