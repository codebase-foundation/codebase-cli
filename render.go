package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ──────────────────────────────────────────────────────────────
//  Tool block rendering
// ──────────────────────────────────────────────────────────────

// renderToolBlock builds a tool execution block with left accent bar.
// State: "pending" (spinner), "success" (✓), "error" (✗)
func renderToolBlock(toolName string, args map[string]any, output string, state string, width int, workDir string) string {
	innerW := width - 3 // left border (1) + padding (1) + breathing room (1)
	if innerW < 20 {
		innerW = 20
	}

	// Header line: ─ toolName ── path ──────────── status ─
	statusIcon := "⣾"
	var style lipgloss.Style
	switch state {
	case "success":
		statusIcon = styleOK.Render("✓")
		style = styleToolSuccess
	case "error":
		statusIcon = styleErr.Render("✗")
		style = styleToolError
	default:
		statusIcon = styleWarn.Render("⣾")
		style = styleToolPending
	}

	// Build the label
	label := styleToolName.Render(toolName)
	detail := ""
	if args != nil {
		if p, ok := args["path"]; ok {
			if s, ok := p.(string); ok {
				detail = s
			}
		}
		if toolName == "search_files" {
			if p, ok := args["pattern"]; ok {
				if s, ok := p.(string); ok {
					detail = s
				}
			}
		}
		if toolName == "git_commit" {
			if m, ok := args["message"]; ok {
				if s, ok := m.(string); ok {
					detail = s
				}
			}
		}
		if toolName == "git_branch" {
			if n, ok := args["name"]; ok {
				if s, ok := n.(string); ok {
					detail = s
				}
			}
		}
		if toolName == "create_task" || toolName == "update_task" {
			if s, ok := args["subject"]; ok {
				if str, ok := s.(string); ok {
					detail = str
				}
			}
		}
		if toolName == "grep" || toolName == "search_files" {
			if p, ok := args["pattern"]; ok {
				if s, ok := p.(string); ok {
					detail = s
				}
			}
		}
		if toolName == "glob" {
			if p, ok := args["pattern"]; ok {
				if s, ok := p.(string); ok {
					detail = s
				}
			}
		}
		if toolName == "shell" {
			if c, ok := args["command"]; ok {
				if s, ok := c.(string); ok {
					detail = s
				}
			}
		}
		if toolName == "web_search" {
			if q, ok := args["query"]; ok {
				if s, ok := q.(string); ok {
					detail = s
				}
			}
		}
		if toolName == "web_fetch" {
			if u, ok := args["url"]; ok {
				if s, ok := u.(string); ok {
					detail = s
				}
			}
		}
		if toolName == "dispatch_agent" {
			if t, ok := args["task"]; ok {
				if s, ok := t.(string); ok {
					detail = s
				}
			}
		}
		if toolName == "notebook_edit" {
			if p, ok := args["path"]; ok {
				if s, ok := p.(string); ok {
					detail = s
				}
			}
		}
	}

	// Truncate detail to fit: " label detail icon" must fit in innerW
	// label + icon + spaces ≈ len(toolName) + 5
	maxDetail := innerW - len(toolName) - 5
	if maxDetail < 10 {
		maxDetail = 10
	}
	if len(detail) > maxDetail {
		detail = detail[:maxDetail-3] + "..."
	}

	// Style the detail and wrap in hyperlink if applicable
	pathStr := ""
	if detail != "" {
		if args != nil {
			if p, ok := args["path"]; ok {
				if s, ok := p.(string); ok && s != "" {
					absPath := detail
					if !filepath.IsAbs(s) && workDir != "" {
						absPath = filepath.Join(workDir, detail)
					}
					pathStr = " " + FileLink(absPath, styleFilePath.Render(detail))
				}
			} else {
				pathStr = " " + styleFilePath.Render(detail)
			}
		} else {
			pathStr = " " + styleFilePath.Render(detail)
		}
	}

	// Build body content
	var body string
	switch toolName {
	case "write_file":
		body = renderFilePreview(args, output, state, innerW)
	case "read_file":
		body = renderReadResult(args, output, state, innerW)
	case "edit_file":
		body = renderEditResult(args, output, state, innerW)
	case "multi_edit":
		body = renderMultiEditResult(args, output, state, innerW)
	case "list_files":
		body = renderListResult(args, output, state, innerW)
	case "search_files":
		body = renderSearchResult(args, output, state, innerW)
	case "dispatch_agent":
		body = renderSubagentResult(args, output, state, innerW)
	case "shell":
		body = renderShellResult(args, output, state, innerW)
	case "grep":
		body = renderSearchResult(args, output, state, innerW)
	case "glob":
		body = renderListResult(args, output, state, innerW)
	case "web_search", "web_fetch":
		body = renderWebResult(toolName, args, output, state, innerW)
	case "notebook_edit":
		body = renderNotebookResult(args, output, state, innerW)
	case "enter_worktree", "exit_worktree":
		body = renderWorktreeResult(toolName, args, output, state, innerW)
	case "enter_plan_mode", "exit_plan_mode":
		body = renderPlanResult(toolName, args, output, state, innerW)
	case "ask_user":
		body = renderAskUserResult(args, output, state, innerW)
	case "config":
		body = renderConfigResult(args, output, state, innerW)
	case "save_memory", "read_memory":
		body = renderMemoryResult(toolName, args, output, state, innerW)
	case "git_status", "git_diff", "git_log", "git_commit", "git_branch":
		body = renderGitResult(toolName, args, output, state, innerW)
	case "create_task", "update_task", "list_tasks", "get_task":
		body = renderTaskResult(toolName, args, output, state, innerW)
	default:
		if output != "" && state != "pending" {
			body = truncateLines(output, 5, innerW)
		}
	}

	// Compose the block
	header := fmt.Sprintf(" %s%s %s", label, pathStr, statusIcon)
	var content string
	if body != "" {
		content = header + "\n" + body
	} else {
		content = header
	}

	return style.Width(innerW).Render(content)
}

// ── File preview (write_file) ────────────────────────────────

func renderFilePreview(args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		return ""
	}
	// Get the content that was written
	if args == nil {
		return styleMuted.Render(" " + output)
	}
	content, ok := args["content"]
	if !ok {
		return styleMuted.Render(" " + output)
	}
	contentStr, ok := content.(string)
	if !ok {
		return styleMuted.Render(" " + output)
	}

	lines := strings.Split(contentStr, "\n")
	maxLines := 8
	var sb strings.Builder
	for i, line := range lines {
		if i >= maxLines {
			remaining := len(lines) - maxLines
			sb.WriteString(styleDim.Render(fmt.Sprintf("     │ ... (%d more lines)", remaining)))
			break
		}
		lineNo := styleLineNo.Render(fmt.Sprintf(" %3d", i+1))
		sep := styleDim.Render(" │ ")
		// Truncate long lines (rune-safe)
		if lipgloss.Width(line) > width-10 {
			runes := []rune(line)
			if len(runes) > width-13 {
				line = string(runes[:width-13]) + "..."
			}
		}
		sb.WriteString(lineNo + sep + line)
		if i < len(lines)-1 || i < maxLines-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// ── Read result ──────────────────────────────────────────────

func renderReadResult(args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		return ""
	}
	lineCount := strings.Count(output, "\n")
	return styleMuted.Render(fmt.Sprintf(" %d lines read", lineCount))
}

// ── Edit result ──────────────────────────────────────────────

func renderEditResult(args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		return ""
	}
	// Show a compact diff view of old → new
	if args != nil && state == "success" {
		oldText, _ := args["old_text"].(string)
		newText, _ := args["new_text"].(string)
		if oldText != "" || newText != "" {
			var sb strings.Builder
			sb.WriteString(styleMuted.Render(" " + output) + "\n")
			maxDiffLines := 4
			// Old lines (red)
			oldLines := strings.Split(oldText, "\n")
			for i, line := range oldLines {
				if i >= maxDiffLines {
					sb.WriteString(styleErr.Render(fmt.Sprintf("   - ... (%d more)", len(oldLines)-maxDiffLines)) + "\n")
					break
				}
				runes := []rune(line)
				if len(runes) > width-6 {
					line = string(runes[:width-9]) + "..."
				}
				sb.WriteString(styleErr.Render("   - "+line) + "\n")
			}
			// New lines (green)
			newLines := strings.Split(newText, "\n")
			for i, line := range newLines {
				if i >= maxDiffLines {
					sb.WriteString(styleOK.Render(fmt.Sprintf("   + ... (%d more)", len(newLines)-maxDiffLines)) + "\n")
					break
				}
				runes := []rune(line)
				if len(runes) > width-6 {
					line = string(runes[:width-9]) + "..."
				}
				sb.WriteString(styleOK.Render("   + "+line) + "\n")
			}
			return strings.TrimRight(sb.String(), "\n")
		}
	}
	return styleMuted.Render(" " + output)
}

// ── Multi-edit result ────────────────────────────────────────

func renderMultiEditResult(args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		return ""
	}
	// Show just the first line (summary)
	lines := strings.Split(output, "\n")
	if len(lines) > 0 {
		return styleMuted.Render(" " + lines[0])
	}
	return styleMuted.Render(" " + output)
}

// ── List result ──────────────────────────────────────────────

func renderListResult(args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		return ""
	}
	lines := strings.Split(output, "\n")
	if len(lines) > 0 {
		return styleMuted.Render(" " + lines[0])
	}
	return styleMuted.Render(" " + output)
}

// ── Search result ────────────────────────────────────────────

func renderSearchResult(args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		return ""
	}
	lines := strings.Split(output, "\n")
	if len(lines) > 0 {
		return styleMuted.Render(" " + lines[0])
	}
	return styleMuted.Render(" " + output)
}

// ── Subagent result ──────────────────────────────────────────

func renderSubagentResult(args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		task := ""
		if args != nil {
			task, _ = args["task"].(string)
		}
		if len(task) > 60 {
			task = task[:57] + "..."
		}
		if task != "" {
			return styleMuted.Render(" " + task)
		}
		return ""
	}
	lines := strings.Split(output, "\n")
	count := len(lines)
	if count > 3 {
		return styleMuted.Render(fmt.Sprintf(" %d lines of research findings", count))
	}
	return truncateLines(output, 3, width)
}

// ── Shell result ─────────────────────────────────────────────

func renderShellResult(args map[string]any, output string, state string, width int) string {
	var cmd string
	if args != nil {
		cmd, _ = args["command"].(string)
	}
	var sb strings.Builder
	sb.WriteString(styleDim.Render(" $ ") + cmd)

	if state == "pending" {
		return sb.String()
	}

	if output != "" {
		sb.WriteString("\n")
		sb.WriteString(truncateLines(output, 10, width))
	}
	return sb.String()
}

// ──────────────────────────────────────────────────────────────
//  Helpers
// ──────────────────────────────────────────────────────────────

func truncateLines(s string, max int, width int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var sb strings.Builder
	for i, line := range lines {
		if i >= max {
			sb.WriteString(styleDim.Render(fmt.Sprintf(" ... (%d more lines)", len(lines)-max)))
			break
		}
		if lipgloss.Width(line) > width-2 {
			// Truncate by runes to avoid cutting multi-byte chars
			runes := []rune(line)
			if len(runes) > width-5 {
				line = string(runes[:width-5]) + "..."
			}
		}
		sb.WriteString(" " + line)
		if i < len(lines)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// wrapText wraps a string to the given width on word boundaries.
func wrapText(s string, width int) string {
	if width <= 0 {
		return s
	}
	var result strings.Builder
	for _, paragraph := range strings.Split(s, "\n") {
		if paragraph == "" {
			result.WriteString("\n")
			continue
		}
		// Preserve leading whitespace
		trimmed := strings.TrimLeft(paragraph, " \t")
		indent := paragraph[:len(paragraph)-len(trimmed)]
		indentW := lipgloss.Width(indent)
		effectiveWidth := width - indentW
		if effectiveWidth < 10 {
			effectiveWidth = 10
		}

		words := strings.Fields(trimmed)
		lineLen := 0
		for i, word := range words {
			wl := lipgloss.Width(word)
			if lineLen+wl+1 > effectiveWidth && lineLen > 0 {
				result.WriteString("\n")
				result.WriteString(indent)
				lineLen = 0
			}
			if lineLen > 0 {
				result.WriteString(" ")
				lineLen++
			} else if i == 0 {
				result.WriteString(indent)
			}
			result.WriteString(word)
			lineLen += wl
		}
		result.WriteString("\n")
	}
	return strings.TrimRight(result.String(), "\n")
}

// ── Task tool rendering ─────────────────────────────────────

func renderTaskResult(toolName string, args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		switch toolName {
		case "create_task":
			subject, _ := args["subject"].(string)
			if subject != "" {
				return styleMuted.Render(" " + subject)
			}
		case "update_task":
			status, _ := args["status"].(string)
			if status != "" {
				return styleMuted.Render(" → " + status)
			}
		}
		return ""
	}
	// For completed task operations, show compact summary
	if output != "" {
		lines := strings.Split(output, "\n")
		if len(lines) > 0 {
			return styleMuted.Render(" " + lines[0])
		}
	}
	return ""
}

// ── Git tool rendering ──────────────────────────────────────

func renderGitResult(toolName string, args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		switch toolName {
		case "git_commit":
			msg, _ := args["message"].(string)
			if len(msg) > 50 {
				msg = msg[:47] + "..."
			}
			return styleMuted.Render(fmt.Sprintf(` "%s"`, msg))
		case "git_branch":
			name, _ := args["name"].(string)
			if name != "" {
				return styleMuted.Render(" " + name)
			}
		}
		return ""
	}
	// Show compact output (first 8 lines)
	return truncateLines(output, 8, width)
}

// ── New tool rendering ──────────────────────────────────────

func renderWebResult(toolName string, args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		if toolName == "web_search" {
			q, _ := args["query"].(string)
			return styleMuted.Render(" searching: " + q)
		}
		u, _ := args["url"].(string)
		return styleMuted.Render(" fetching: " + u)
	}
	return truncateLines(output, 6, width)
}

func renderNotebookResult(args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		mode, _ := args["edit_mode"].(string)
		if mode == "" {
			mode = "replace"
		}
		return styleMuted.Render(" " + mode + " cell")
	}
	return truncateLines(output, 3, width)
}

func renderWorktreeResult(toolName string, args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		if toolName == "enter_worktree" {
			return styleMuted.Render(" creating worktree...")
		}
		return styleMuted.Render(" cleaning up worktree...")
	}
	return truncateLines(output, 4, width)
}

func renderPlanResult(toolName string, args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		if toolName == "enter_plan_mode" {
			return styleMuted.Render(" entering plan mode...")
		}
		return styleMuted.Render(" presenting plan...")
	}
	return truncateLines(output, 6, width)
}

func renderAskUserResult(args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		q, _ := args["question"].(string)
		if q != "" && len(q) > 60 {
			q = q[:57] + "..."
		}
		return styleMuted.Render(" " + q)
	}
	return truncateLines(output, 8, width)
}

func renderConfigResult(args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		action, _ := args["action"].(string)
		key, _ := args["key"].(string)
		return styleMuted.Render(" " + action + " " + key)
	}
	return truncateLines(output, 4, width)
}

func renderMemoryResult(toolName string, args map[string]any, output string, state string, width int) string {
	if state == "pending" {
		fn, _ := args["filename"].(string)
		if toolName == "save_memory" {
			return styleMuted.Render(" saving: " + fn)
		}
		return styleMuted.Render(" reading: " + fn)
	}
	return truncateLines(output, 4, width)
}
