package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

// UserPrompter presents questions to the user and returns answers.
// Implemented by the TUI layer and injected via Env.
type UserPrompter interface {
	// AskUser presents a question and returns the answer.
	// For non-interactive contexts, returns an error.
	AskUser(question string) (string, error)
}

// AskUser asks the user a clarifying question before making assumptions.
// Prevents wrong-direction coding by confirming intent. The model can use
// this when requirements are ambiguous.
type AskUser struct{}

func (AskUser) Name() string                          { return "ask_user" }
func (AskUser) ConcurrencySafe(_ map[string]any) bool { return false }
func (AskUser) Effects() []tool.Effect                { return nil }

func (AskUser) Description() string {
	return "Ask the user a clarifying question before proceeding. " +
		"Use this when requirements are ambiguous, you need a design decision, " +
		"or there are multiple valid approaches and user preference matters. " +
		"Provide clear options when possible."
}

func (AskUser) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"question": {
				"type": "string",
				"description": "The question to ask. Should be clear, specific, and end with a question mark."
			},
			"options": {
				"type": "array",
				"description": "2-4 options for the user to choose from. Each with a label and description.",
				"items": {
					"type": "object",
					"properties": {
						"label": {
							"type": "string",
							"description": "Short option label (1-5 words)."
						},
						"description": {
							"type": "string",
							"description": "What this option means."
						}
					},
					"required": ["label", "description"]
				}
			},
			"context": {
				"type": "string",
				"description": "Brief context for why you're asking (what you've learned so far)."
			}
		},
		"required": ["question"]
	}`)
}

func (AskUser) Execute(_ context.Context, args map[string]any, _ *tool.Env) tool.Result {
	question, _ := args["question"].(string)
	if question == "" {
		return tool.Result{Output: "Error: question is required", Success: false}
	}

	// Format the question with options for the TUI to display
	var sb strings.Builder
	sb.WriteString("## Question for User\n\n")

	if ctx, ok := args["context"].(string); ok && ctx != "" {
		sb.WriteString(ctx + "\n\n")
	}

	sb.WriteString(question + "\n")

	if opts, ok := args["options"].([]any); ok && len(opts) > 0 {
		sb.WriteString("\nOptions:\n")
		for i, opt := range opts {
			if o, ok := opt.(map[string]any); ok {
				label, _ := o["label"].(string)
				desc, _ := o["description"].(string)
				sb.WriteString(fmt.Sprintf("  %d. **%s** — %s\n", i+1, label, desc))
			}
		}
	}

	// The tool result is displayed to the user by the TUI.
	// The user's response comes as the next message in the conversation.
	// This is how CC does it too — the tool doesn't block for an answer,
	// it presents the question and the model waits for user input.
	return tool.Result{Output: sb.String(), Success: true}
}
