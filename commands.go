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
			sb.WriteString(styleDim.Render("  Tokens:   ") + styleMuted.Render(fmt.Sprintf("%d", totalTokens)) + "\n")
			sb.WriteString(styleDim.Render("  Files:    ") + styleMuted.Render(fmt.Sprintf("%d modified", m.files)) + "\n")
			sb.WriteString(styleDim.Render("  Turns:    ") + styleMuted.Render(fmt.Sprintf("%d", m.turns)) + "\n")
			sb.WriteString(styleDim.Render("  History:  ") + styleMuted.Render(fmt.Sprintf("%d messages", histLen)) + "\n")
			sb.WriteString(styleDim.Render("  WorkDir:  ") + styleMuted.Render(m.config.WorkDir) + "\n")
			if m.title != "" {
				sb.WriteString(styleDim.Render("  Title:    ") + styleMuted.Render(m.title) + "\n")
			}
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
		name: "theme",
		desc: "Switch color theme (usage: /theme dark|light)",
		handler: func(m *chatModel, args string) tea.Cmd {
			if args == "" {
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleMuted.Render("  Current theme: ") + styleAccentText.Render(activeTheme.Name) + "\n" +
						styleMuted.Render("  Usage: /theme dark|light\n"),
				})
				m.rebuildViewport()
				return nil
			}
			switch strings.ToLower(args) {
			case "dark", "light":
				setTheme(args)
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleOK.Render("  ✓ ") + styleMuted.Render("Switched to "+activeTheme.Name+" theme.\n"),
				})
				m.rebuildViewport()
			default:
				m.segments = append(m.segments, segment{
					kind: "text",
					text: styleWarn.Render("  Unknown theme: "+args) + styleMuted.Render(". Try: dark, light\n"),
				})
				m.rebuildViewport()
			}
			return nil
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
