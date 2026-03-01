package main

import (
	"strings"
	"testing"
)

func TestParseAnswerNumeric(t *testing.T) {
	q := &PlanQuestion{
		Options: []PlanOption{
			{ID: "opt1", Label: "JWT tokens"},
			{ID: "opt2", Label: "Session cookies"},
			{ID: "opt3", Label: "OAuth2"},
		},
	}

	answer := ParseAnswer("1", q)
	if answer != "JWT tokens" {
		t.Errorf("expected 'JWT tokens', got %q", answer)
	}

	answer = ParseAnswer("3", q)
	if answer != "OAuth2" {
		t.Errorf("expected 'OAuth2', got %q", answer)
	}
}

func TestParseAnswerMultiSelect(t *testing.T) {
	q := &PlanQuestion{
		Type: "multiselect",
		Options: []PlanOption{
			{ID: "opt1", Label: "Dark mode"},
			{ID: "opt2", Label: "Auth system"},
			{ID: "opt3", Label: "API layer"},
		},
	}

	answer := ParseAnswer("1,3", q)
	if answer != "Dark mode, API layer" {
		t.Errorf("expected 'Dark mode, API layer', got %q", answer)
	}
}

func TestParseAnswerFreeText(t *testing.T) {
	q := &PlanQuestion{
		Options: []PlanOption{
			{ID: "opt1", Label: "Option A"},
		},
	}

	answer := ParseAnswer("I want something custom", q)
	if answer != "I want something custom" {
		t.Errorf("expected free text, got %q", answer)
	}
}

func TestParseAnswerEmpty(t *testing.T) {
	q := &PlanQuestion{}
	answer := ParseAnswer("", q)
	if answer != "" {
		t.Errorf("expected empty, got %q", answer)
	}
}

func TestParseAnswerOutOfRange(t *testing.T) {
	q := &PlanQuestion{
		Options: []PlanOption{
			{ID: "opt1", Label: "Only option"},
		},
	}

	// Out of range number falls through to free text
	answer := ParseAnswer("5", q)
	if answer != "5" {
		t.Errorf("expected '5' as free text, got %q", answer)
	}
}

func TestFormatQuestion(t *testing.T) {
	q := &PlanQuestion{
		ID:       "q1",
		Question: "What auth method?",
		Type:     "select",
		Options: []PlanOption{
			{ID: "opt1", Label: "JWT", Description: "Stateless"},
			{ID: "opt2", Label: "Sessions", Description: "Traditional"},
		},
	}

	formatted := FormatQuestion(q, 1)

	if !strings.Contains(formatted, "Question 1") {
		t.Error("should contain question number")
	}
	if !strings.Contains(formatted, "What auth method?") {
		t.Error("should contain question text")
	}
	if !strings.Contains(formatted, "[1] JWT") {
		t.Error("should contain option 1")
	}
	if !strings.Contains(formatted, "[2] Sessions") {
		t.Error("should contain option 2")
	}
	if !strings.Contains(formatted, "Stateless") {
		t.Error("should contain option description")
	}
}

func TestFormatQuestionMultiselect(t *testing.T) {
	q := &PlanQuestion{
		Type: "multiselect",
		Options: []PlanOption{
			{ID: "opt1", Label: "A"},
			{ID: "opt2", Label: "B"},
		},
	}

	formatted := FormatQuestion(q, 2)
	if !strings.Contains(formatted, "commas") {
		t.Error("multiselect should mention comma-separated input")
	}
}

func TestBuildPlanPrompt(t *testing.T) {
	qa := []QAPair{
		{Question: "What framework?", Answer: "React"},
		{Question: "What style?", Answer: "Tailwind"},
	}

	prompt := BuildPlanPrompt("build a dashboard", "# Plan\nStep 1: Setup\nStep 2: Build", qa)

	if !strings.Contains(prompt, "build a dashboard") {
		t.Error("should contain original prompt")
	}
	if !strings.Contains(prompt, "# Plan") {
		t.Error("should contain the plan")
	}
	if !strings.Contains(prompt, "React") {
		t.Error("should contain Q&A answers")
	}
	if !strings.Contains(prompt, "Follow the plan") {
		t.Error("should contain instructions")
	}
}

func TestBuildPlanPromptNoQA(t *testing.T) {
	prompt := BuildPlanPrompt("build something", "# Plan", nil)

	if !strings.Contains(prompt, "build something") {
		t.Error("should contain original prompt")
	}
	if strings.Contains(prompt, "User preferences") {
		t.Error("should not contain Q&A section when no QA history")
	}
}

func TestPlanStateInit(t *testing.T) {
	ps := &PlanState{
		OriginalPrompt: "build auth",
	}

	if ps.Done {
		t.Error("should not be done initially")
	}
	if ps.QuestionCount != 0 {
		t.Error("should start with 0 questions")
	}
	if len(ps.QAHistory) != 0 {
		t.Error("should start with empty QA history")
	}
}
