package main

import (
	"math"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/charmbracelet/lipgloss"
)

// ──────────────────────────────────────────────────────────────
//  PHOSPHOR — color palette
// ──────────────────────────────────────────────────────────────

var (
	colBg        = lipgloss.Color("#0d1117")
	colSurface   = lipgloss.Color("#161b22")
	colBorder    = lipgloss.Color("#30363d")
	colBorderHi  = lipgloss.Color("#58a6ff")
	colText      = lipgloss.Color("#e6edf3")
	colSecondary = lipgloss.Color("#7d8590")
	colDim       = lipgloss.Color("#484f58")
	colAccent    = lipgloss.Color("#58a6ff")
	colSuccess   = lipgloss.Color("#3fb950")
	colWarning   = lipgloss.Color("#d29922")
	colError     = lipgloss.Color("#f85149")
	colPurple    = lipgloss.Color("#a371f7")
	colOrange    = lipgloss.Color("#f0883e")
	colCyan      = lipgloss.Color("#56d4dd")
)

// ──────────────────────────────────────────────────────────────
//  Reusable styles
// ──────────────────────────────────────────────────────────────

// Main outer frame (idle)
var styleFrame = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colBorder).
	Padding(0, 1)

// Main outer frame (streaming / active)
var styleFrameActive = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colBorderHi).
	Padding(0, 1)

// Main outer frame (done flash)
var styleFrameDone = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colSuccess).
	Padding(0, 1)

// Tool block — pending (spinner visible)
var styleToolPending = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colOrange)

// Tool block — success
var styleToolSuccess = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colSuccess)

// Tool block — error
var styleToolError = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(colError)

// User prompt prefix
var styleUserLabel = lipgloss.NewStyle().
	Foreground(colAccent).
	Bold(true)

// Agent text
var styleAgentText = lipgloss.NewStyle().
	Foreground(colText)

// Muted / secondary text
var styleMuted = lipgloss.NewStyle().
	Foreground(colSecondary)

// Dim text
var styleDim = lipgloss.NewStyle().
	Foreground(colDim)

// File path
var styleFilePath = lipgloss.NewStyle().
	Foreground(colCyan)

// Line numbers in file preview
var styleLineNo = lipgloss.NewStyle().
	Foreground(colDim)

// Success indicator
var styleOK = lipgloss.NewStyle().
	Foreground(colSuccess).
	Bold(true)

// Error indicator
var styleErr = lipgloss.NewStyle().
	Foreground(colError).
	Bold(true)

// Warning
var styleWarn = lipgloss.NewStyle().
	Foreground(colWarning)

// Purple (thinking)
var styleThinking = lipgloss.NewStyle().
	Foreground(colPurple).
	Italic(true)

// Orange (tool name)
var styleToolName = lipgloss.NewStyle().
	Foreground(colOrange).
	Bold(true)

// Status bar
var styleStatusBar = lipgloss.NewStyle().
	Foreground(colSecondary)

// Boot title
var styleBootTitle = lipgloss.NewStyle().
	Foreground(colAccent).
	Bold(true)

// Boot label (left side of dot lines)
var styleBootLabel = lipgloss.NewStyle().
	Foreground(colSecondary)

// Boot value (right side of dot lines)
var styleBootValue = lipgloss.NewStyle().
	Foreground(colText)

// Boot dots
var styleBootDots = lipgloss.NewStyle().
	Foreground(colDim)

// Boot check mark
var styleBootCheck = lipgloss.NewStyle().
	Foreground(colSuccess)

// Prompt character
var stylePromptChar = lipgloss.NewStyle().
	Foreground(colAccent).
	Bold(true)

// ──────────────────────────────────────────────────────────────
//  Demoscene — starfield, copper bars, color cycling
// ──────────────────────────────────────────────────────────────

// Star layer foreground colors (far → near)
var (
	colStarFar  = lipgloss.Color("#333344")
	colStarMid  = lipgloss.Color("#667788")
	colStarNear = lipgloss.Color("#bbccdd")
)

// buildRetroPalette returns 16 C64/Amiga-inspired hex colors for cycling.
func buildRetroPalette() []string {
	hexColors := []string{
		"#ff0055", // hot pink
		"#ff3300", // red-orange
		"#ff6600", // orange
		"#ffaa00", // amber
		"#ffdd00", // yellow
		"#aaff00", // yellow-green
		"#00ff55", // green
		"#00ffaa", // teal
		"#00ddff", // cyan
		"#0088ff", // sky blue
		"#0044ff", // blue
		"#4400ff", // indigo
		"#8800ff", // purple
		"#cc00ff", // magenta
		"#ff00cc", // deep pink
		"#ff0088", // rose
	}
	return hexColors
}

// buildCopperGradient creates a symmetric gradient of hex colors for a copper bar.
// The gradient goes from hex1 at the edges to hex2 at the center.
func buildCopperGradient(hex1, hex2 string, steps int) []string {
	c1, _ := colorful.Hex(hex1)
	c2, _ := colorful.Hex(hex2)
	grad := make([]string, steps)
	for i := 0; i < steps; i++ {
		t := float64(i) / float64(steps-1)
		grad[i] = c1.BlendHcl(c2, t).Clamped().Hex()
	}
	return grad
}

// lerpColor blends two hex colors by t (0.0 = hex1, 1.0 = hex2).
func lerpColor(hex1, hex2 string, t float64) string {
	c1, _ := colorful.Hex(hex1)
	c2, _ := colorful.Hex(hex2)
	t = math.Max(0, math.Min(1, t))
	return c1.BlendHcl(c2, t).Clamped().Hex()
}

// fgStyle returns a lipgloss style with the given hex foreground color.
func fgStyle(hex string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(hex))
}

// fgBgStyle returns a lipgloss style with foreground and background hex colors.
func fgBgStyle(fg, bg string) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(fg)).
		Background(lipgloss.Color(bg))
}

// bgStyle returns a lipgloss style with only the given hex background color.
func bgStyle(hex string) lipgloss.Style {
	return lipgloss.NewStyle().Background(lipgloss.Color(hex))
}

