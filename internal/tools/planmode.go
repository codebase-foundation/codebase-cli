package tools

import (
	"context"
	"encoding/json"

	"github.com/codebase-foundation/cli/internal/tool"
)

// ── enter_plan_mode ─────────────────────────────────────────

// EnterPlanMode transitions to read-only exploration mode.
// The model reads code and designs before implementing. Produces
// better code by forcing architectural thinking first.
type EnterPlanMode struct{}

func (EnterPlanMode) Name() string                          { return "enter_plan_mode" }
func (EnterPlanMode) ConcurrencySafe(_ map[string]any) bool { return false }
func (EnterPlanMode) Effects() []tool.Effect                { return nil }

func (EnterPlanMode) Description() string {
	return "Enter plan mode for structured exploration and design before coding. " +
		"In plan mode, focus on reading code, understanding the architecture, and designing a solution. " +
		"Use this before complex implementations to think through the approach."
}

func (EnterPlanMode) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"reason": {
				"type": "string",
				"description": "Why entering plan mode — what needs to be designed/explored."
			}
		}
	}`)
}

func (EnterPlanMode) Execute(_ context.Context, args map[string]any, _ *tool.Env) tool.Result {
	reason, _ := args["reason"].(string)
	msg := "Entered plan mode. Focus on reading and understanding code before making changes.\n\n"
	msg += "In plan mode:\n"
	msg += "- Read files and explore the codebase\n"
	msg += "- Search for relevant patterns and dependencies\n"
	msg += "- Design your approach before implementing\n"
	msg += "- Use exit_plan_mode when ready to implement\n"
	if reason != "" {
		msg += "\nPlanning: " + reason
	}
	return tool.Result{Output: msg, Success: true}
}

// ── exit_plan_mode ──────────────────────────────────────────

// ExitPlanMode exits plan mode, optionally presenting the plan.
type ExitPlanMode struct{}

func (ExitPlanMode) Name() string                          { return "exit_plan_mode" }
func (ExitPlanMode) ConcurrencySafe(_ map[string]any) bool { return false }
func (ExitPlanMode) Effects() []tool.Effect                { return nil }

func (ExitPlanMode) Description() string {
	return "Exit plan mode and present the implementation plan. " +
		"Summarize what was learned and the planned approach before starting to code."
}

func (ExitPlanMode) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"plan": {
				"type": "string",
				"description": "The implementation plan — what to build, in what order, and why."
			}
		},
		"required": ["plan"]
	}`)
}

func (ExitPlanMode) Execute(_ context.Context, args map[string]any, _ *tool.Env) tool.Result {
	plan, _ := args["plan"].(string)
	if plan == "" {
		return tool.Result{Output: "Error: plan is required when exiting plan mode", Success: false}
	}
	return tool.Result{
		Output:  "Exited plan mode. Ready to implement.\n\n## Plan\n\n" + plan,
		Success: true,
	}
}
