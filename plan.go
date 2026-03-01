package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ──────────────────────────────────────────────────────────────
//  Planning Pipeline — Q&A + Plan Generation
//
//  Before complex tasks, the Glue SMART model asks clarifying
//  questions, then generates a structured plan. The user can
//  review and revise the plan before the agent starts.
//
//  Flow: classify → Q&A loop → generate plan → review → agent
// ──────────────────────────────────────────────────────────────

const minQuestions = 1
const maxQuestions = 5

// PlanQuestion is a clarifying question with options.
type PlanQuestion struct {
	ID       string       `json:"id"`
	Question string       `json:"question"`
	Type     string       `json:"type"` // "select" or "multiselect"
	Options  []PlanOption `json:"options"`
}

// PlanOption is a selectable answer option.
type PlanOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// QAPair stores a question and its answer.
type QAPair struct {
	Question string
	Answer   string
}

// PlanState tracks the planning session.
type PlanState struct {
	OriginalPrompt string
	QAHistory      []QAPair
	CurrentQ       *PlanQuestion
	QuestionCount  int
	Plan           string // generated markdown plan
	Done           bool   // Q&A phase complete
}

// ──────────────────────────────────────────────────────────────
//  Question Generation
// ──────────────────────────────────────────────────────────────

const questionPrompt = `You are helping a user plan a coding project. Ask ONE clarifying question to understand their requirements better.

Rules:
- Ask about architecture, design, features, scope, or technical preferences
- Provide 2-4 concrete options (with short descriptions)
- Use "select" for mutually exclusive choices, "multiselect" when multiple apply
- Each question should build on previous answers
- After enough context (usually 2-4 questions), set "done": true

Respond as JSON:
{
  "done": false,
  "question": {
    "id": "q1",
    "question": "What authentication approach do you want?",
    "type": "select",
    "options": [
      {"id": "opt1", "label": "JWT tokens", "description": "Stateless, good for APIs"},
      {"id": "opt2", "label": "Session cookies", "description": "Traditional, simpler setup"},
      {"id": "opt3", "label": "OAuth2", "description": "Third-party login (Google, GitHub)"}
    ]
  }
}

Or when you have enough context:
{
  "done": true,
  "summary": "Brief description of what will be built"
}`

// GenerateQuestion creates the next clarifying question based on context.
func (g *GlueClient) GenerateQuestion(prompt string, qaHistory []QAPair, questionNum int) (*PlanQuestion, bool, string) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("User's request: %q\n\n", prompt))

	if len(qaHistory) > 0 {
		sb.WriteString("Previous Q&A:\n")
		for i, qa := range qaHistory {
			sb.WriteString(fmt.Sprintf("Q%d: %s\nA%d: %s\n\n", i+1, qa.Question, i+1, qa.Answer))
		}
	}

	sb.WriteString(fmt.Sprintf("This is question %d of max %d. ", questionNum, maxQuestions))
	if questionNum >= maxQuestions {
		sb.WriteString("This is the last question — set done=true if you have enough context, or ask the final question.")
	}

	messages := []ChatMessage{
		{Role: "system", Content: strPtr(questionPrompt)},
		{Role: "user", Content: strPtr(sb.String())},
	}

	result, err := nonStreamingChat(g.smart, messages)
	if err != nil {
		return nil, true, "Planning questions unavailable, proceeding with your request as-is."
	}

	// Parse JSON
	var parsed struct {
		Done     bool          `json:"done"`
		Summary  string        `json:"summary"`
		Question *PlanQuestion `json:"question"`
	}

	result = strings.TrimSpace(result)
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		// Try extracting JSON from messy response
		if idx := strings.Index(result, "{"); idx >= 0 {
			if end := strings.LastIndex(result, "}"); end > idx {
				json.Unmarshal([]byte(result[idx:end+1]), &parsed)
			}
		}
	}

	// Enforce minimum questions
	if parsed.Done && questionNum < minQuestions {
		parsed.Done = false
	}

	// Enforce maximum
	if !parsed.Done && questionNum >= maxQuestions {
		parsed.Done = true
		if parsed.Summary == "" {
			parsed.Summary = fmt.Sprintf("Building based on %d answers", len(qaHistory))
		}
	}

	if parsed.Done {
		return nil, true, parsed.Summary
	}

	if parsed.Question == nil {
		return nil, true, "Ready to build."
	}

	return parsed.Question, false, ""
}

// ──────────────────────────────────────────────────────────────
//  Plan Generation
// ──────────────────────────────────────────────────────────────

const planGenPrompt = `You are writing a project implementation plan that will guide a coding agent. Based on the user's request and their answers to clarifying questions, create a concise plan.

Output a clean markdown document with these sections:
# Overview
(1-2 sentences: what we're building)

# Key Decisions
(Bullet list of choices made during Q&A)

# Implementation Steps
(Numbered list of concrete steps the agent should follow)

# Files to Create/Modify
(List each file with a brief description of its purpose)

Be specific and actionable. The coding agent will follow this plan step by step.`

// GeneratePlan creates a markdown implementation plan from the Q&A session.
func (g *GlueClient) GeneratePlan(prompt string, qaHistory []QAPair) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("User's request: %q\n\n", prompt))

	if len(qaHistory) > 0 {
		sb.WriteString("Clarifying Q&A:\n")
		for i, qa := range qaHistory {
			sb.WriteString(fmt.Sprintf("Q%d: %s\nA%d: %s\n\n", i+1, qa.Question, i+1, qa.Answer))
		}
	}

	sb.WriteString("Write the implementation plan.")

	messages := []ChatMessage{
		{Role: "system", Content: strPtr(planGenPrompt)},
		{Role: "user", Content: strPtr(sb.String())},
	}

	result, err := nonStreamingChat(g.smart, messages)
	if err != nil {
		return "Plan generation failed. Proceeding with original request."
	}

	return strings.TrimSpace(result)
}

// ──────────────────────────────────────────────────────────────
//  Plan Revision
// ──────────────────────────────────────────────────────────────

const planRevisePrompt = `You are revising a project implementation plan based on user feedback. Apply their changes and return the COMPLETE revised plan in the same markdown format. Do not explain what changed — just output the revised plan.`

// RevisePlan updates the plan based on user feedback.
func (g *GlueClient) RevisePlan(currentPlan, feedback string) string {
	messages := []ChatMessage{
		{Role: "system", Content: strPtr(planRevisePrompt)},
		{Role: "user", Content: strPtr(fmt.Sprintf("Current plan:\n%s\n\nUser's feedback:\n%q\n\nOutput the revised plan:", currentPlan, feedback))},
	}

	result, err := nonStreamingChat(g.smart, messages)
	if err != nil {
		return currentPlan // keep original on error
	}

	return strings.TrimSpace(result)
}

// ──────────────────────────────────────────────────────────────
//  Build Prompt Assembly
// ──────────────────────────────────────────────────────────────

// BuildPlanPrompt creates the enriched prompt for the agent, including the plan.
func BuildPlanPrompt(originalPrompt, plan string, qaHistory []QAPair) string {
	var sb strings.Builder

	sb.WriteString("Build this project. Follow the approved plan exactly.\n\n")
	sb.WriteString(fmt.Sprintf("Original request: %q\n\n", originalPrompt))

	if len(qaHistory) > 0 {
		sb.WriteString("User preferences (from Q&A):\n")
		for i, qa := range qaHistory {
			sb.WriteString(fmt.Sprintf("Q%d: %s → %s\n", i+1, qa.Question, qa.Answer))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## Implementation Plan\n\n")
	sb.WriteString(plan)
	sb.WriteString("\n\nFollow the plan step by step. Implement every listed item. Keep going until all files are written and the project is complete. Do not stop after partial work.")

	return sb.String()
}

// FormatQuestion renders a PlanQuestion for the TUI viewport.
func FormatQuestion(q *PlanQuestion, questionNum int) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("  Question %d: %s\n\n", questionNum, q.Question))

	for i, opt := range q.Options {
		label := fmt.Sprintf("  [%d] %s", i+1, opt.Label)
		if opt.Description != "" {
			label += " — " + opt.Description
		}
		sb.WriteString(label + "\n")
	}

	hint := "\n  Type a number to select"
	if q.Type == "multiselect" {
		hint = "\n  Type numbers separated by commas (e.g. 1,3)"
	}
	hint += ", or type a custom answer\n"
	sb.WriteString(hint)

	return sb.String()
}

// ParseAnswer interprets user input as option selection(s) or free text.
func ParseAnswer(input string, q *PlanQuestion) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return input
	}

	// Try to parse as number(s)
	parts := strings.Split(input, ",")
	var selectedLabels []string

	for _, part := range parts {
		part = strings.TrimSpace(part)
		var idx int
		if _, err := fmt.Sscanf(part, "%d", &idx); err == nil {
			if idx >= 1 && idx <= len(q.Options) {
				selectedLabels = append(selectedLabels, q.Options[idx-1].Label)
			}
		}
	}

	if len(selectedLabels) > 0 {
		return strings.Join(selectedLabels, ", ")
	}

	// Free text answer
	return input
}
