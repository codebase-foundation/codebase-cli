package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ──────────────────────────────────────────────────────────────
//  Setup wizard — first-run interactive configuration
// ──────────────────────────────────────────────────────────────

type setupStep int

const (
	stepProvider       setupStep = iota // arrow-key: choose main provider
	stepCustomURL                       // text input: custom base URL
	stepAPIKey                          // text input: API key
	stepFetchModels                     // spinner: querying /v1/models
	stepModel                           // arrow-key: pick model from list
	stepGlueChoice                      // arrow-key: Same/Different/Skip
	stepGlueProvider                    // arrow-key: choose glue provider
	stepGlueCustomURL                   // text input: custom glue base URL
	stepGlueAPIKey                      // text input: glue API key
	stepGlueFetchModels                 // spinner: fetch glue models
	stepGlueFastModel                   // arrow-key: pick fast model
	stepGlueSmartModel                  // arrow-key: pick smart model
	stepDone                            // save + transition
)

type providerPreset struct {
	name    string
	baseURL string
	keyHint string
}

var providers = []providerPreset{
	{name: "Login with Codebase", baseURL: "__codebase_login__", keyHint: "browser login"},
	{name: "OpenAI", baseURL: "https://api.openai.com/v1", keyHint: "sk-..."},
	{name: "Anthropic", baseURL: "https://api.anthropic.com", keyHint: "sk-ant-..."},
	{name: "OpenRouter", baseURL: "https://openrouter.ai/api/v1", keyHint: "sk-or-..."},
	{name: "Custom URL", baseURL: "", keyHint: ""},
}

var glueChoices = []string{"Same provider", "Different provider", "Skip"}

// Messages
type modelsFetchedMsg struct {
	models []string
	err    error
	glue   bool // true if this fetch was for glue models
}

type setupDoneMsg struct {
	config savedConfig
}

type setupModel struct {
	step    setupStep
	width   int
	height  int
	spinner spinner.Model
	input   textinput.Model

	// Main provider
	providerIdx int
	baseURL     string
	apiKey      string
	models      []string
	modelIdx    int
	fetchErr    string // shown when model fetch fails

	// Glue
	glueChoice     int // 0=same, 1=different, 2=skip
	glueProviderIdx int
	glueBaseURL    string
	glueAPIKey     string
	glueModels     []string
	glueFastIdx    int
	glueSmartIdx   int

	// For manual model entry when fetch fails
	manualEntry bool
}

func newSetupModel() setupModel {
	s := spinner.New()
	s.Spinner = spinner.Dot

	ti := textinput.New()
	ti.CharLimit = 256
	ti.Width = 60

	return setupModel{
		step:    stepProvider,
		spinner: s,
		input:   ti,
	}
}

func (m setupModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, textinput.Blink)
}

func (m setupModel) Update(msg tea.Msg) (setupModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case modelsFetchedMsg:
		return m.handleModelsFetched(msg)
	}

	// Forward to text input when active
	if m.isTextStep() {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m setupModel) isTextStep() bool {
	return m.step == stepCustomURL || m.step == stepAPIKey ||
		m.step == stepGlueCustomURL || m.step == stepGlueAPIKey ||
		(m.step == stepModel && m.manualEntry) ||
		(m.step == stepGlueFastModel && m.manualEntry) ||
		(m.step == stepGlueSmartModel && m.manualEntry)
}

func (m setupModel) handleKey(msg tea.KeyMsg) (setupModel, tea.Cmd) {
	key := msg.String()

	// Global escape
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	switch m.step {
	case stepProvider:
		newIdx, entered := arrowSelect(key, len(providers), m.providerIdx)
		m.providerIdx = newIdx
		if entered {
			p := providers[m.providerIdx]

			// Codebase login — launch browser OAuth flow
			if p.baseURL == "__codebase_login__" {
				return m, func() tea.Msg {
					err := Login()
					if err != nil {
						return modelsFetchedMsg{err: err}
					}
					// Login succeeded — load credentials and configure
					return setupDoneMsg{config: savedConfig{
						APIKey:  "__codebase_oauth__",
						BaseURL: oauthBaseURL + "/inference",
						Model:   "MiniMax-M2.7",
					}}
				}
			}

			if p.baseURL == "" {
				m.step = stepCustomURL
				m.input.SetValue("")
				m.input.Placeholder = "https://your-api.example.com/v1"
				m.input.Focus()
				return m, textinput.Blink
			}
			m.baseURL = p.baseURL
			m.step = stepAPIKey
			m.input.SetValue("")
			m.input.Placeholder = p.keyHint
			m.input.EchoMode = textinput.EchoPassword
			m.input.Focus()
			return m, textinput.Blink
		}
		return m, nil

	case stepCustomURL:
		if key == "enter" {
			val := strings.TrimSpace(m.input.Value())
			if val == "" {
				return m, nil
			}
			m.baseURL = strings.TrimSuffix(val, "/")
			m.step = stepAPIKey
			m.input.SetValue("")
			m.input.Placeholder = "your-api-key"
			m.input.EchoMode = textinput.EchoPassword
			m.input.Focus()
			return m, textinput.Blink
		}
		if key == "esc" {
			m.step = stepProvider
			m.input.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case stepAPIKey:
		if key == "enter" {
			val := strings.TrimSpace(m.input.Value())
			if val == "" {
				return m, nil
			}
			m.apiKey = val
			m.input.Blur()
			m.input.EchoMode = textinput.EchoNormal
			m.step = stepFetchModels
			m.fetchErr = ""
			return m, tea.Batch(m.spinner.Tick, m.fetchModelsCmd(false))
		}
		if key == "esc" {
			if providers[m.providerIdx].baseURL == "" {
				m.step = stepCustomURL
			} else {
				m.step = stepProvider
			}
			m.input.Blur()
			m.input.EchoMode = textinput.EchoNormal
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case stepFetchModels, stepGlueFetchModels:
		// Just wait for the fetch result, but allow esc to skip
		if key == "esc" {
			if m.step == stepFetchModels {
				m.step = stepModel
				m.manualEntry = true
				m.input.SetValue("")
				m.input.Placeholder = "type model name (e.g. gpt-4o)"
				m.input.Focus()
				return m, textinput.Blink
			}
			m.step = stepGlueFastModel
			m.manualEntry = true
			m.input.SetValue("")
			m.input.Placeholder = "type model name"
			m.input.Focus()
			return m, textinput.Blink
		}
		return m, nil

	case stepModel:
		if m.manualEntry {
			if key == "enter" {
				val := strings.TrimSpace(m.input.Value())
				if val == "" {
					return m, nil
				}
				m.models = []string{val}
				m.modelIdx = 0
				m.manualEntry = false
				m.input.Blur()
				m.step = stepGlueChoice
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		newIdx, entered := arrowSelect(key, len(m.models), m.modelIdx)
		m.modelIdx = newIdx
		if entered {
			m.step = stepGlueChoice
		}
		return m, nil

	case stepGlueChoice:
		newIdx, entered := arrowSelect(key, len(glueChoices), m.glueChoice)
		m.glueChoice = newIdx
		if entered {
			switch m.glueChoice {
			case 0: // Same provider
				m.glueBaseURL = m.baseURL
				m.glueAPIKey = m.apiKey
				m.step = stepGlueFetchModels
				m.fetchErr = ""
				return m, tea.Batch(m.spinner.Tick, m.fetchModelsCmd(true))
			case 1: // Different provider
				m.step = stepGlueProvider
				m.glueProviderIdx = 0
				return m, nil
			case 2: // Skip
				return m, m.saveAndFinish()
			}
		}
		return m, nil

	case stepGlueProvider:
		newIdx, entered := arrowSelect(key, len(providers), m.glueProviderIdx)
		m.glueProviderIdx = newIdx
		if entered {
			p := providers[m.glueProviderIdx]
			if p.baseURL == "" {
				m.step = stepGlueCustomURL
				m.input.SetValue("")
				m.input.Placeholder = "https://your-api.example.com/v1"
				m.input.Focus()
				return m, textinput.Blink
			}
			m.glueBaseURL = p.baseURL
			m.step = stepGlueAPIKey
			m.input.SetValue("")
			m.input.Placeholder = p.keyHint
			m.input.EchoMode = textinput.EchoPassword
			m.input.Focus()
			return m, textinput.Blink
		}
		return m, nil

	case stepGlueCustomURL:
		if key == "enter" {
			val := strings.TrimSpace(m.input.Value())
			if val == "" {
				return m, nil
			}
			m.glueBaseURL = strings.TrimSuffix(val, "/")
			m.step = stepGlueAPIKey
			m.input.SetValue("")
			m.input.Placeholder = "your-api-key"
			m.input.EchoMode = textinput.EchoPassword
			m.input.Focus()
			return m, textinput.Blink
		}
		if key == "esc" {
			m.step = stepGlueProvider
			m.input.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case stepGlueAPIKey:
		if key == "enter" {
			val := strings.TrimSpace(m.input.Value())
			if val == "" {
				return m, nil
			}
			m.glueAPIKey = val
			m.input.Blur()
			m.input.EchoMode = textinput.EchoNormal
			m.step = stepGlueFetchModels
			m.fetchErr = ""
			return m, tea.Batch(m.spinner.Tick, m.fetchModelsCmd(true))
		}
		if key == "esc" {
			if providers[m.glueProviderIdx].baseURL == "" {
				m.step = stepGlueCustomURL
			} else {
				m.step = stepGlueProvider
			}
			m.input.Blur()
			m.input.EchoMode = textinput.EchoNormal
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case stepGlueFastModel:
		if m.manualEntry {
			if key == "enter" {
				val := strings.TrimSpace(m.input.Value())
				if val == "" {
					return m, nil
				}
				m.glueModels = []string{val}
				m.glueFastIdx = 0
				m.manualEntry = false
				m.input.Blur()
				// Move to smart model — also manual
				m.step = stepGlueSmartModel
				m.manualEntry = true
				m.input.SetValue("")
				m.input.Placeholder = "type smart model name"
				m.input.Focus()
				return m, textinput.Blink
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		newIdx, entered := arrowSelect(key, len(m.glueModels), m.glueFastIdx)
		m.glueFastIdx = newIdx
		if entered {
			m.step = stepGlueSmartModel
			m.glueSmartIdx = 0
			for i, model := range m.glueModels {
				lower := strings.ToLower(model)
				if strings.Contains(lower, "70b") || strings.Contains(lower, "4o") ||
					strings.Contains(lower, "sonnet") || strings.Contains(lower, "4.1") {
					m.glueSmartIdx = i
					break
				}
			}
		}
		return m, nil

	case stepGlueSmartModel:
		if m.manualEntry {
			if key == "enter" {
				val := strings.TrimSpace(m.input.Value())
				if val == "" {
					return m, nil
				}
				m.glueSmartIdx = 0
				m.input.Blur()
				m.manualEntry = false
				sc := m.buildSavedConfig()
				sc.GlueSmartModel = val
				return m, func() tea.Msg { return setupDoneMsg{config: sc} }
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		newIdx, entered := arrowSelect(key, len(m.glueModels), m.glueSmartIdx)
		m.glueSmartIdx = newIdx
		if entered {
			return m, m.saveAndFinish()
		}
		return m, nil
	}

	return m, nil
}

// arrowSelect handles arrow/vim key navigation. Returns the new index and
// whether Enter was pressed. This is a free function (not a method) to avoid
// the Go value-receiver copy bug where pointer modifications to a caller's
// struct field are lost when the method returns its own stale copy of m.
func arrowSelect(key string, count int, idx int) (int, bool) {
	switch key {
	case "up", "k", "left", "h":
		if idx > 0 {
			idx--
		}
	case "down", "j", "right", "l":
		if idx < count-1 {
			idx++
		}
	case "enter":
		return idx, true
	}
	return idx, false
}

func (m setupModel) handleModelsFetched(msg modelsFetchedMsg) (setupModel, tea.Cmd) {
	if msg.glue {
		if msg.err != nil {
			m.fetchErr = msg.err.Error()
			m.step = stepGlueFastModel
			m.manualEntry = true
			m.input.SetValue("")
			m.input.Placeholder = "type model name (e.g. llama-3.1-8b-instant)"
			m.input.Focus()
			return m, textinput.Blink
		}
		m.glueModels = msg.models
		m.glueFastIdx = 0
		m.glueSmartIdx = 0
		m.fetchErr = ""
		m.step = stepGlueFastModel
		return m, nil
	}

	// Main models
	if msg.err != nil {
		m.fetchErr = msg.err.Error()
		m.step = stepModel
		m.manualEntry = true
		m.input.SetValue("")
		m.input.Placeholder = "type model name (e.g. gpt-4o)"
		m.input.Focus()
		return m, textinput.Blink
	}
	m.models = msg.models
	m.modelIdx = 0
	m.fetchErr = ""
	m.step = stepModel
	return m, nil
}

func (m setupModel) fetchModelsCmd(glue bool) tea.Cmd {
	apiKey := m.apiKey
	baseURL := m.baseURL
	if glue {
		apiKey = m.glueAPIKey
		baseURL = m.glueBaseURL
	}
	protocol := detectProtocol(baseURL)
	return func() tea.Msg {
		models, err := listModels(apiKey, baseURL, protocol)
		return modelsFetchedMsg{models: models, err: err, glue: glue}
	}
}

func (m setupModel) buildSavedConfig() savedConfig {
	sc := savedConfig{
		APIKey:  m.apiKey,
		BaseURL: m.baseURL,
		Model:   m.models[m.modelIdx],
	}
	if m.glueChoice != 2 && len(m.glueModels) > 0 { // not "skip"
		sc.GlueAPIKey = m.glueAPIKey
		sc.GlueBaseURL = m.glueBaseURL
		if m.glueFastIdx < len(m.glueModels) {
			sc.GlueFastModel = m.glueModels[m.glueFastIdx]
		}
		if m.glueSmartIdx < len(m.glueModels) {
			sc.GlueSmartModel = m.glueModels[m.glueSmartIdx]
		}
	}
	return sc
}

func (m setupModel) saveAndFinish() tea.Cmd {
	sc := m.buildSavedConfig()
	return func() tea.Msg {
		return setupDoneMsg{config: sc}
	}
}

// ──────────────────────────────────────────────────────────────
//  View
// ──────────────────────────────────────────────────────────────

func (m setupModel) View() string {
	w := m.width
	if w < 40 {
		w = 80
	}

	var out strings.Builder

	// Title
	out.WriteString("\n")
	out.WriteString(styleAccentText.Render("  Welcome to Codebase!") + "\n")
	out.WriteString(styleDim.Render("  Setup wizard — configure your AI provider") + "\n\n")

	// Steps indicator
	totalSteps := m.totalSteps()
	currentStep := m.currentStepNum()
	out.WriteString(m.renderStepIndicator(currentStep, totalSteps) + "\n\n")

	switch m.step {
	case stepProvider:
		out.WriteString(styleAccentText.Render("  Step 1: Main Provider") + "\n")
		out.WriteString(styleDim.Render("  Choose your AI provider") + "\n\n")
		for i, p := range providers {
			if i == m.providerIdx {
				out.WriteString(lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("  ▸ "+p.name) + "\n")
			} else {
				out.WriteString(styleDim.Render("    "+p.name) + "\n")
			}
		}

	case stepCustomURL:
		out.WriteString(styleAccentText.Render("  Step 1: Custom Base URL") + "\n")
		out.WriteString(styleDim.Render("  Enter your OpenAI-compatible API base URL") + "\n\n")
		out.WriteString("  " + m.input.View() + "\n")

	case stepAPIKey:
		provName := providers[m.providerIdx].name
		out.WriteString(styleAccentText.Render("  Step 2: API Key") + "\n")
		out.WriteString(styleDim.Render("  Enter your "+provName+" API key") + "\n\n")
		out.WriteString("  " + m.input.View() + "\n")

	case stepFetchModels:
		out.WriteString(styleAccentText.Render("  Step 3: Model") + "\n")
		out.WriteString("  " + m.spinner.View() + styleMuted.Render(" Fetching available models...") + "\n")
		out.WriteString(styleDim.Render("  (press esc to type manually)") + "\n")

	case stepModel:
		out.WriteString(styleAccentText.Render("  Step 3: Model") + "\n")
		if m.fetchErr != "" {
			out.WriteString(styleWarn.Render("  Could not fetch models: "+truncateForDisplay(m.fetchErr, 60)) + "\n")
			out.WriteString(styleDim.Render("  Type your model name manually:") + "\n\n")
			out.WriteString("  " + m.input.View() + "\n")
		} else if m.manualEntry {
			out.WriteString(styleDim.Render("  Type your model name:") + "\n\n")
			out.WriteString("  " + m.input.View() + "\n")
		} else {
			out.WriteString(styleDim.Render("  Select your main model") + "\n\n")
			out.WriteString(m.renderModelList(m.models, m.modelIdx, 12))
		}

	case stepGlueChoice:
		out.WriteString(styleAccentText.Render("  Step 4: Glue Setup") + "\n")
		out.WriteString(styleMuted.Render("  Glue models handle background tasks like intent classification,") + "\n")
		out.WriteString(styleMuted.Render("  progress narration, and session titles. Using a cheap/fast model") + "\n")
		out.WriteString(styleMuted.Render("  here keeps your main model costs down.") + "\n\n")
		for i, c := range glueChoices {
			if i == m.glueChoice {
				out.WriteString(lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("  ▸ "+c) + "\n")
			} else {
				out.WriteString(styleDim.Render("    "+c) + "\n")
			}
		}

	case stepGlueProvider:
		out.WriteString(styleAccentText.Render("  Step 4: Glue Provider") + "\n")
		out.WriteString(styleDim.Render("  Choose a provider for glue models (cheap/fast recommended)") + "\n\n")
		for i, p := range providers {
			if i == m.glueProviderIdx {
				out.WriteString(lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("  ▸ "+p.name) + "\n")
			} else {
				out.WriteString(styleDim.Render("    "+p.name) + "\n")
			}
		}

	case stepGlueCustomURL:
		out.WriteString(styleAccentText.Render("  Step 4: Glue Custom URL") + "\n")
		out.WriteString(styleDim.Render("  Enter the base URL for your glue provider") + "\n\n")
		out.WriteString("  " + m.input.View() + "\n")

	case stepGlueAPIKey:
		provName := providers[m.glueProviderIdx].name
		out.WriteString(styleAccentText.Render("  Step 4: Glue API Key") + "\n")
		out.WriteString(styleDim.Render("  Enter your "+provName+" API key for glue") + "\n\n")
		out.WriteString("  " + m.input.View() + "\n")

	case stepGlueFetchModels:
		out.WriteString(styleAccentText.Render("  Step 5: Glue Models") + "\n")
		out.WriteString("  " + m.spinner.View() + styleMuted.Render(" Fetching available models...") + "\n")
		out.WriteString(styleDim.Render("  (press esc to type manually)") + "\n")

	case stepGlueFastModel:
		out.WriteString(styleAccentText.Render("  Step 5: Fast Model") + "\n")
		out.WriteString(styleMuted.Render("  Used for narration, titles, and quick responses.") + "\n")
		out.WriteString(styleMuted.Render("  Pick something cheap and fast (8B params is plenty).") + "\n\n")
		if m.fetchErr != "" {
			out.WriteString(styleWarn.Render("  Could not fetch models: "+truncateForDisplay(m.fetchErr, 60)) + "\n")
			out.WriteString(styleDim.Render("  Type your model name manually:") + "\n\n")
			out.WriteString("  " + m.input.View() + "\n")
		} else if m.manualEntry {
			out.WriteString(styleDim.Render("  Type your fast model name:") + "\n\n")
			out.WriteString("  " + m.input.View() + "\n")
		} else {
			out.WriteString(m.renderModelList(m.glueModels, m.glueFastIdx, 12))
		}

	case stepGlueSmartModel:
		out.WriteString(styleAccentText.Render("  Step 6: Smart Model") + "\n")
		out.WriteString(styleMuted.Render("  Used for intent classification and planning questions.") + "\n")
		out.WriteString(styleMuted.Render("  Needs to be a bit smarter (70B+ recommended).") + "\n\n")
		if m.manualEntry {
			out.WriteString(styleDim.Render("  Type your smart model name:") + "\n\n")
			out.WriteString("  " + m.input.View() + "\n")
		} else {
			out.WriteString(m.renderModelList(m.glueModels, m.glueSmartIdx, 12))
		}

	case stepDone:
		out.WriteString(styleOK.Render("  ✓ Configuration saved to ~/.codebase/config.json") + "\n")
	}

	// Hints
	out.WriteString("\n")
	switch {
	case m.isTextStep():
		out.WriteString(styleDim.Render("  enter confirm  ·  esc back") + "\n")
	case m.step == stepFetchModels || m.step == stepGlueFetchModels:
		out.WriteString(styleDim.Render("  esc type manually") + "\n")
	default:
		out.WriteString(styleDim.Render("  ↑↓ select  ·  enter confirm  ·  ctrl+c quit") + "\n")
	}

	return out.String()
}

func (m setupModel) renderModelList(models []string, selected int, maxVisible int) string {
	if len(models) == 0 {
		return styleDim.Render("  No models available\n")
	}

	// Scroll window: keep selected item visible
	start := 0
	if selected >= maxVisible {
		start = selected - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(models) {
		end = len(models)
	}

	var sb strings.Builder
	if start > 0 {
		sb.WriteString(styleDim.Render(fmt.Sprintf("  ↑ %d more\n", start)))
	}
	for i := start; i < end; i++ {
		if i == selected {
			sb.WriteString(lipgloss.NewStyle().Foreground(colAccent).Bold(true).Render("  ▸ "+models[i]) + "\n")
		} else {
			sb.WriteString(styleDim.Render("    "+models[i]) + "\n")
		}
	}
	if end < len(models) {
		sb.WriteString(styleDim.Render(fmt.Sprintf("  ↓ %d more\n", len(models)-end)))
	}
	return sb.String()
}

func (m setupModel) renderStepIndicator(current, total int) string {
	var parts []string
	for i := 1; i <= total; i++ {
		if i < current {
			parts = append(parts, styleOK.Render("●"))
		} else if i == current {
			parts = append(parts, styleAccentText.Render("●"))
		} else {
			parts = append(parts, styleDim.Render("○"))
		}
	}
	return "  " + strings.Join(parts, " ")
}

func (m setupModel) currentStepNum() int {
	switch m.step {
	case stepProvider, stepCustomURL:
		return 1
	case stepAPIKey:
		return 2
	case stepFetchModels, stepModel:
		return 3
	case stepGlueChoice, stepGlueProvider, stepGlueCustomURL, stepGlueAPIKey:
		return 4
	case stepGlueFetchModels, stepGlueFastModel:
		return 5
	case stepGlueSmartModel:
		return 6
	case stepDone:
		return 6
	}
	return 1
}

func (m setupModel) totalSteps() int {
	switch {
	case m.step <= stepModel:
		return 4 // don't know about glue yet
	case m.glueChoice == 2:
		return 4 // skip glue
	default:
		return 6
	}
}

func truncateForDisplay(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}
