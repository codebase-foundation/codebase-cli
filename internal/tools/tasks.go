package tools

import (
	"context"
	"encoding/json"

	"github.com/codebase-foundation/cli/internal/tool"
)

// ── create_task ──────────────────────────────────────────────

type CreateTask struct{}

func (CreateTask) Name() string                          { return "create_task" }
func (CreateTask) ConcurrencySafe(_ map[string]any) bool { return false }
func (CreateTask) Effects() []tool.Effect                { return nil }
func (CreateTask) Description() string {
	return "Create a task to track progress. The user sees these as a live checklist."
}
func (CreateTask) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"subject": {"type": "string", "description": "Short imperative task title."},
			"description": {"type": "string", "description": "What needs to be done."},
			"active_form": {"type": "string", "description": "Present continuous form (e.g. 'Adding auth') shown in spinner."}
		},
		"required": ["subject"]
	}`)
}
func (CreateTask) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	if env.Tasks == nil {
		return tool.Result{Output: "Error: task manager not available", Success: false}
	}
	output, ok := env.Tasks.CreateTask(args)
	return tool.Result{Output: output, Success: ok}
}

// ── update_task ──────────────────────────────────────────────

type UpdateTask struct{}

func (UpdateTask) Name() string                          { return "update_task" }
func (UpdateTask) ConcurrencySafe(_ map[string]any) bool { return false }
func (UpdateTask) Effects() []tool.Effect                { return nil }
func (UpdateTask) Description() string {
	return "Update task status (pending → in_progress → completed)."
}
func (UpdateTask) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Task ID to update."},
			"status": {"type": "string", "description": "New status: pending, in_progress, or completed."}
		},
		"required": ["id", "status"]
	}`)
}
func (UpdateTask) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	if env.Tasks == nil {
		return tool.Result{Output: "Error: task manager not available", Success: false}
	}
	output, ok := env.Tasks.UpdateTask(args)
	return tool.Result{Output: output, Success: ok}
}

// ── list_tasks ───────────────────────────────────────────────

type ListTasks struct{}

func (ListTasks) Name() string                          { return "list_tasks" }
func (ListTasks) ConcurrencySafe(_ map[string]any) bool { return true }
func (ListTasks) Effects() []tool.Effect                { return nil }
func (ListTasks) Description() string   { return "List all tasks with status." }
func (ListTasks) Schema() json.RawMessage {
	return tool.MustSchema(`{"type":"object","properties":{}}`)
}
func (ListTasks) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	if env.Tasks == nil {
		return tool.Result{Output: "Error: task manager not available", Success: false}
	}
	output, ok := env.Tasks.ListTasks(args)
	return tool.Result{Output: output, Success: ok}
}

// ── get_task ─────────────────────────────────────────────────

type GetTask struct{}

func (GetTask) Name() string                          { return "get_task" }
func (GetTask) ConcurrencySafe(_ map[string]any) bool { return true }
func (GetTask) Effects() []tool.Effect                { return nil }
func (GetTask) Description() string  { return "Get full details of a specific task." }
func (GetTask) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"id": {"type": "string", "description": "Task ID."}
		},
		"required": ["id"]
	}`)
}
func (GetTask) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	if env.Tasks == nil {
		return tool.Result{Output: "Error: task manager not available", Success: false}
	}
	output, ok := env.Tasks.GetTask(args)
	return tool.Result{Output: output, Success: ok}
}
