package main

import (
	"fmt"
	"os"
)

// ──────────────────────────────────────────────────────────────
//  VS Code / Cursor terminal integration
//
//  Detects IDE terminal environment and provides OSC escape
//  sequence helpers for file hyperlinks, shell integration,
//  and other terminal features.
// ──────────────────────────────────────────────────────────────

// TerminalInfo holds detected terminal capabilities.
type TerminalInfo struct {
	IsVSCode       bool
	IsCursor       bool
	Program        string // TERM_PROGRAM value
	SupportsOSC8   bool   // hyperlinks
	SupportsOSC633 bool   // VS Code shell integration
}

var termInfo TerminalInfo

func init() {
	termInfo.Program = os.Getenv("TERM_PROGRAM")
	termInfo.IsVSCode = termInfo.Program == "vscode"
	termInfo.IsCursor = termInfo.Program == "cursor"
	// Most modern terminals support OSC 8 hyperlinks
	termInfo.SupportsOSC8 = termInfo.IsVSCode || termInfo.IsCursor ||
		termInfo.Program == "iTerm.app" || termInfo.Program == "WezTerm" ||
		termInfo.Program == "ghostty" || termInfo.Program == "kitty"
	termInfo.SupportsOSC633 = termInfo.IsVSCode || termInfo.IsCursor
}

// TerminalName returns a human-readable terminal description for display.
func TerminalName() string {
	if termInfo.IsVSCode {
		return "VS Code (hyperlinks enabled)"
	}
	if termInfo.IsCursor {
		return "Cursor (hyperlinks enabled)"
	}
	if termInfo.Program != "" {
		suffix := ""
		if termInfo.SupportsOSC8 {
			suffix = " (hyperlinks enabled)"
		}
		return termInfo.Program + suffix
	}
	return "terminal"
}

// OSC8Link wraps text in an OSC 8 hyperlink escape sequence.
// Returns plain text if the terminal doesn't support it.
func OSC8Link(url, text string) string {
	if !termInfo.SupportsOSC8 {
		return text
	}
	return "\033]8;;" + url + "\033\\" + text + "\033]8;;\033\\"
}

// FileLink creates a clickable file:// hyperlink for an absolute path.
func FileLink(absPath, displayText string) string {
	return OSC8Link("file://"+absPath, displayText)
}

// OSC633PromptStart emits the VS Code shell integration prompt-start marker.
func OSC633PromptStart() string {
	if !termInfo.SupportsOSC633 {
		return ""
	}
	return "\033]633;A\033\\"
}

// OSC633PromptEnd emits the VS Code shell integration prompt-end marker.
func OSC633PromptEnd() string {
	if !termInfo.SupportsOSC633 {
		return ""
	}
	return "\033]633;B\033\\"
}

// OSC633CommandStart emits the VS Code shell integration command-start marker.
func OSC633CommandStart() string {
	if !termInfo.SupportsOSC633 {
		return ""
	}
	return "\033]633;C\033\\"
}

// OSC633CommandEnd emits the VS Code shell integration command-end marker.
func OSC633CommandEnd(exitCode int) string {
	if !termInfo.SupportsOSC633 {
		return ""
	}
	return fmt.Sprintf("\033]633;D;%d\033\\", exitCode)
}
