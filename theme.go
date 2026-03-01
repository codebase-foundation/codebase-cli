package main

import (
	"math"
	"os"
	"strings"

	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/charmbracelet/lipgloss"
)

// ──────────────────────────────────────────────────────────────
//  Theme — switchable color palettes (dark / light)
// ──────────────────────────────────────────────────────────────

// Theme holds all semantic colors for the TUI.
type Theme struct {
	Name      string
	Bg        string
	Surface   string
	Border    string
	BorderHi  string
	Text      string
	Secondary string
	Dim       string
	Accent    string
	Success   string
	Warning   string
	Error     string
	Purple    string
	Orange    string
	Cyan      string
}

func themeDark() Theme {
	return Theme{
		Name:      "dark",
		Bg:        "#0d1117",
		Surface:   "#161b22",
		Border:    "#30363d",
		BorderHi:  "#58a6ff",
		Text:      "#e6edf3",
		Secondary: "#7d8590",
		Dim:       "#484f58",
		Accent:    "#58a6ff",
		Success:   "#3fb950",
		Warning:   "#d29922",
		Error:     "#f85149",
		Purple:    "#a371f7",
		Orange:    "#f0883e",
		Cyan:      "#56d4dd",
	}
}

func themeLight() Theme {
	return Theme{
		Name:      "light",
		Bg:        "#ffffff",
		Surface:   "#f6f8fa",
		Border:    "#d0d7de",
		BorderHi:  "#0969da",
		Text:      "#1f2328",
		Secondary: "#656d76",
		Dim:       "#8b949e",
		Accent:    "#0969da",
		Success:   "#1a7f37",
		Warning:   "#9a6700",
		Error:     "#d1242f",
		Purple:    "#8250df",
		Orange:    "#bc4c00",
		Cyan:      "#0598bc",
	}
}

// activeTheme is the current color palette.
var activeTheme = themeDark()

// ──────────────────────────────────────────────────────────────
//  Color vars — populated from activeTheme by initStyles()
// ──────────────────────────────────────────────────────────────

var (
	colBg        lipgloss.Color
	colSurface   lipgloss.Color
	colBorder    lipgloss.Color
	colBorderHi  lipgloss.Color
	colText      lipgloss.Color
	colSecondary lipgloss.Color
	colDim       lipgloss.Color
	colAccent    lipgloss.Color
	colSuccess   lipgloss.Color
	colWarning   lipgloss.Color
	colError     lipgloss.Color
	colPurple    lipgloss.Color
	colOrange    lipgloss.Color
	colCyan      lipgloss.Color
)

// ──────────────────────────────────────────────────────────────
//  Style vars — rebuilt from colors by initStyles()
// ──────────────────────────────────────────────────────────────

var (
	styleFrame       lipgloss.Style
	styleFrameActive lipgloss.Style
	styleFrameDone   lipgloss.Style
	styleFramePlan   lipgloss.Style
	styleToolPending lipgloss.Style
	styleToolSuccess lipgloss.Style
	styleToolError   lipgloss.Style
	styleUserLabel   lipgloss.Style
	styleAgentText   lipgloss.Style
	styleMuted       lipgloss.Style
	styleDim         lipgloss.Style
	styleFilePath    lipgloss.Style
	styleLineNo      lipgloss.Style
	styleOK          lipgloss.Style
	styleErr         lipgloss.Style
	styleWarn        lipgloss.Style
	styleThinking    lipgloss.Style
	styleToolName    lipgloss.Style
	styleStatusBar   lipgloss.Style
	styleBootTitle   lipgloss.Style
	styleBootLabel   lipgloss.Style
	styleBootValue   lipgloss.Style
	styleBootDots    lipgloss.Style
	styleBootCheck   lipgloss.Style
	stylePromptChar  lipgloss.Style
	styleAccentText  lipgloss.Style
)

// initTheme reads CODEBASE_THEME env var and initializes all colors+styles.
func initTheme() {
	name := strings.ToLower(os.Getenv("CODEBASE_THEME"))
	switch name {
	case "light":
		activeTheme = themeLight()
	default:
		activeTheme = themeDark()
	}
	initStyles()
}

// setTheme switches the active theme and rebuilds all styles.
func setTheme(name string) {
	switch strings.ToLower(name) {
	case "light":
		activeTheme = themeLight()
	default:
		activeTheme = themeDark()
	}
	initStyles()
}

// initStyles populates all col* and style* vars from activeTheme.
func initStyles() {
	t := activeTheme

	// Colors
	colBg = lipgloss.Color(t.Bg)
	colSurface = lipgloss.Color(t.Surface)
	colBorder = lipgloss.Color(t.Border)
	colBorderHi = lipgloss.Color(t.BorderHi)
	colText = lipgloss.Color(t.Text)
	colSecondary = lipgloss.Color(t.Secondary)
	colDim = lipgloss.Color(t.Dim)
	colAccent = lipgloss.Color(t.Accent)
	colSuccess = lipgloss.Color(t.Success)
	colWarning = lipgloss.Color(t.Warning)
	colError = lipgloss.Color(t.Error)
	colPurple = lipgloss.Color(t.Purple)
	colOrange = lipgloss.Color(t.Orange)
	colCyan = lipgloss.Color(t.Cyan)

	// Frame styles
	styleFrame = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBorder).
		Padding(0, 1)
	styleFrameActive = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colBorderHi).
		Padding(0, 1)
	styleFrameDone = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colSuccess).
		Padding(0, 1)
	styleFramePlan = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colPurple).
		Padding(0, 1)

	// Tool block styles
	styleToolPending = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colOrange)
	styleToolSuccess = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colSuccess)
	styleToolError = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colError)

	// Text styles
	styleUserLabel = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleAgentText = lipgloss.NewStyle().Foreground(colText)
	styleMuted = lipgloss.NewStyle().Foreground(colSecondary)
	styleDim = lipgloss.NewStyle().Foreground(colDim)
	styleFilePath = lipgloss.NewStyle().Foreground(colCyan)
	styleLineNo = lipgloss.NewStyle().Foreground(colDim)

	// Indicators
	styleOK = lipgloss.NewStyle().Foreground(colSuccess).Bold(true)
	styleErr = lipgloss.NewStyle().Foreground(colError).Bold(true)
	styleWarn = lipgloss.NewStyle().Foreground(colWarning)
	styleThinking = lipgloss.NewStyle().Foreground(colPurple).Italic(true)

	// Misc
	styleToolName = lipgloss.NewStyle().Foreground(colOrange).Bold(true)
	styleStatusBar = lipgloss.NewStyle().Foreground(colSecondary)
	stylePromptChar = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleAccentText = lipgloss.NewStyle().Foreground(colAccent).Bold(true)

	// Boot
	styleBootTitle = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleBootLabel = lipgloss.NewStyle().Foreground(colSecondary)
	styleBootValue = lipgloss.NewStyle().Foreground(colText)
	styleBootDots = lipgloss.NewStyle().Foreground(colDim)
	styleBootCheck = lipgloss.NewStyle().Foreground(colSuccess)
}

func init() {
	// Initialize with defaults so styles are usable even without calling initTheme()
	initTheme()
}

// ──────────────────────────────────────────────────────────────
//  Color helpers
// ──────────────────────────────────────────────────────────────

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

