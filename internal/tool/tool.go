// Package tool defines the core Tool interface and associated types.
//
// Every tool — local, MCP, or subagent — implements this interface.
// The agent loop doesn't know or care where a tool comes from.
package tool

import (
	"context"
	"encoding/json"
)

// ──────────────────────────────────────────────────────────────
//  Effects — what a tool does (for permission policies)
// ──────────────────────────────────────────────────────────────

// Effect describes a side-effect category. Tools declare their effects;
// the permission engine matches policies against effects — never tool names.
type Effect string

const (
	EffectReadsFS  Effect = "reads_fs"      // reads files from the filesystem
	EffectWritesFS Effect = "writes_fs"     // creates or modifies files
	EffectRunsProc Effect = "runs_process"  // spawns a subprocess
	EffectNetwork  Effect = "network"       // makes network requests
	EffectGitRead  Effect = "git_read"      // reads git state (log, status, diff)
	EffectGitWrite Effect = "git_write"     // modifies git state (commit, branch)
)

// ──────────────────────────────────────────────────────────────
//  Tool result
// ──────────────────────────────────────────────────────────────

// Result is what a tool returns after execution.
type Result struct {
	Output  string   // text output sent to the LLM
	Success bool     // did it succeed?
	Files   []string // files touched (for diagnostics, rendering hints)
}

// ──────────────────────────────────────────────────────────────
//  Service interfaces — optional deps available through Env
// ──────────────────────────────────────────────────────────────

// SubagentRunner spawns a read-only research subagent.
type SubagentRunner interface {
	RunSubagent(task string) (string, error)
}

// TaskManager provides task tracking for multi-step work.
type TaskManager interface {
	CreateTask(args map[string]any) (string, bool)
	UpdateTask(args map[string]any) (string, bool)
	ListTasks(args map[string]any) (string, bool)
	GetTask(args map[string]any) (string, bool)
}

// GlueRunner provides access to the fast/cheap sidecar LLM for
// classification, summarization, and other lightweight tasks.
// This is a differentiator over CC — tools can use glue for smart
// decisions without burning main model tokens.
type GlueRunner interface {
	// Classify sends a short prompt to the fast model and returns the response.
	// Use for: read-only command classification, result summarization,
	// content extraction, error explanation.
	Classify(ctx context.Context, prompt string) (string, error)
}

// ──────────────────────────────────────────────────────────────
//  Tool environment — passed to Execute
// ──────────────────────────────────────────────────────────────

// FileSnapshotter saves file state before modifications (for undo).
type FileSnapshotter interface {
	Snapshot(absPath, relPath string, turn int)
}

// Env carries the execution environment for tools. Every tool gets this;
// tools that don't need optional services just ignore the nil fields.
type Env struct {
	WorkDir string // project root directory
	Turn    int    // current agent turn number

	// Optional services — nil when not configured
	Subagent SubagentRunner  // for dispatch_agent
	Tasks    TaskManager     // for task tools
	Glue     GlueRunner      // for smart classification (shell, web_fetch, etc.)
	History  FileSnapshotter  // for auto-snapshotting before edits
}

// ──────────────────────────────────────────────────────────────
//  Tool interface
// ──────────────────────────────────────────────────────────────

// Tool is the core abstraction. Local tools, MCP tools, and subagent
// tools all implement this interface identically.
type Tool interface {
	// Name is the tool's identifier as seen by the LLM (e.g. "read_file").
	Name() string

	// Description is the human/LLM-readable explanation of what the tool does.
	Description() string

	// Schema returns the JSON Schema for the tool's arguments.
	// This is provider-neutral — no OpenAI or Anthropic wrappers.
	// The registry handles provider-specific formatting.
	//
	// Example:
	//   {"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}
	Schema() json.RawMessage

	// Effects declares what this tool does. The permission engine uses
	// these to decide whether to ask the user for approval.
	Effects() []Effect

	// ConcurrencySafe returns true if this specific invocation can safely
	// run in parallel with other concurrent-safe invocations.
	//
	// This is INPUT-DEPENDENT. The same tool can be safe for some args
	// and unsafe for others. Example: shell("ls") is safe, shell("rm -rf")
	// is not. Tools that are always safe (read_file) can ignore args.
	// Tools that are always unsafe (write_file) can ignore args.
	//
	// When in doubt, return false — serial execution is always correct.
	ConcurrencySafe(args map[string]any) bool

	// Execute runs the tool with the given arguments.
	// Args have already been validated against Schema() by the registry.
	// Env carries the working directory and optional services.
	Execute(ctx context.Context, args map[string]any, env *Env) Result
}

// ──────────────────────────────────────────────────────────────
//  Schema helpers for tool implementations
// ──────────────────────────────────────────────────────────────

// MustSchema is a convenience for tool implementations to define inline schemas.
// Panics on invalid JSON (caught at init time, not runtime).
func MustSchema(s string) json.RawMessage {
	var js json.RawMessage
	if err := json.Unmarshal([]byte(s), &js); err != nil {
		panic("tool: invalid schema JSON: " + err.Error())
	}
	return js
}
