package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ──────────────────────────────────────────────────────────────
//  Tool block rendering
// ──────────────────────────────────────────────────────────────

// renderToolBlock builds a bordered tool execution block.
// State: "pending" (spinner), "success" (✓), "error" (✗)
func renderToolBlock(toolName string, args map[string]any, output string, state string, width int) string {
	innerW := width - 4 // border + padding
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
	pathStr := ""
	if args != nil {
		if p, ok := args["path"]; ok {
			if s, ok := p.(string); ok {
				pathStr = " " + styleFilePath.Render(s)
			}
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
	case "shell":
		body = renderShellResult(args, output, state, innerW)
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

	return style.Width(innerW + 2).Render(content)
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
		// Truncate long lines
		if len(line) > width-10 {
			line = line[:width-13] + "..."
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
	return styleMuted.Render(" " + output)
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
		if len(line) > width-2 {
			line = line[:width-5] + "..."
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
		words := strings.Fields(paragraph)
		lineLen := 0
		for i, word := range words {
			wl := len(word)
			if lineLen+wl+1 > width && lineLen > 0 {
				result.WriteString("\n")
				lineLen = 0
			}
			if lineLen > 0 {
				result.WriteString(" ")
				lineLen++
			} else if i > 0 {
				// start of new wrapped line
			}
			result.WriteString(word)
			lineLen += wl
		}
		result.WriteString("\n")
	}
	return strings.TrimRight(result.String(), "\n")
}
