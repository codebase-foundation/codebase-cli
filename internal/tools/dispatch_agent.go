package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/codebase-foundation/cli/internal/tool"
)

// DispatchAgent spawns a subagent with configurable type, isolation, and model.
//
// Agent types:
//   - "explore" (default) — read-only codebase exploration specialist
//   - "plan" — software architect for designing implementation plans
//   - "general-purpose" — full access agent for delegated coding tasks
//
// Isolation:
//   - "" (default) — runs in same working directory
//   - "worktree" — creates a temporary git worktree for isolated changes
type DispatchAgent struct{}

func (DispatchAgent) Name() string { return "dispatch_agent" }
func (DispatchAgent) Effects() []tool.Effect {
	return []tool.Effect{tool.EffectReadsFS}
}

func (DispatchAgent) Description() string {
	return "Spawn a subagent to handle a task autonomously. " +
		"Agent types: 'explore' (default, read-only codebase search), " +
		"'plan' (architecture/design, read-only), " +
		"'general-purpose' (full access for delegated coding tasks). " +
		"Set isolation='worktree' to run in an isolated git worktree " +
		"(changes don't affect main checkout until merged). " +
		"Use 'explore' for quick research. Use 'general-purpose' with 'worktree' " +
		"for parallel coding tasks that shouldn't conflict."
}

func (DispatchAgent) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"task": {
				"type": "string",
				"description": "The task for the subagent. Be specific about what to investigate or build."
			},
			"type": {
				"type": "string",
				"description": "Agent type: 'explore' (read-only search, default), 'plan' (architecture design), 'general-purpose' (full coding access).",
				"enum": ["explore", "plan", "general-purpose"]
			},
			"isolation": {
				"type": "string",
				"description": "Isolation mode. 'worktree' creates a temporary git worktree so the agent works on an isolated copy of the repo.",
				"enum": ["worktree"]
			},
			"model": {
				"type": "string",
				"description": "Optional model override for this agent (e.g. 'claude-haiku-4-5' for fast exploration)."
			}
		},
		"required": ["task"]
	}`)
}

// ConcurrencySafe: explore and plan agents are read-only and safe to parallelize.
// general-purpose agents write files and are NOT safe.
func (DispatchAgent) ConcurrencySafe(args map[string]any) bool {
	agentType, _ := args["type"].(string)
	if agentType == "" {
		agentType = "explore"
	}
	return agentType == "explore" || agentType == "plan"
}

func (DispatchAgent) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	task, _ := args["task"].(string)
	if task == "" {
		return tool.Result{Output: "Error: task is required", Success: false}
	}
	if env.Subagent == nil {
		return tool.Result{Output: "Error: subagent runner not available", Success: false}
	}

	agentType, _ := args["type"].(string)
	if agentType == "" {
		agentType = "explore"
	}
	isolation, _ := args["isolation"].(string)
	model, _ := args["model"].(string)

	// Build config string for the runner
	// The SubagentRunner interface is simple (task string) → (result, error)
	// We encode the config into the task string as a JSON prefix that the
	// runner can parse. This avoids changing the interface.
	config := map[string]string{
		"type":      agentType,
		"isolation": isolation,
		"model":     model,
	}
	configJSON, _ := json.Marshal(config)
	enrichedTask := fmt.Sprintf("__agent_config__:%s\n%s", string(configJSON), task)

	result, err := env.Subagent.RunSubagent(enrichedTask)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Subagent error: %v", err), Success: false}
	}
	return tool.Result{Output: result, Success: true}
}
