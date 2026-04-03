package tools

import "github.com/codebase-foundation/cli/internal/tool"

// RegisterAll registers all built-in tools with the given registry.
// Called once at startup. 27 tools — matching CC's core set.
func RegisterAll(r *tool.Registry) {
	// ── Filesystem — read ────────────────────────────────────
	r.Register(ReadFile{})
	r.Register(ListFiles{})
	r.Register(SearchFiles{})
	r.Register(Glob{})
	r.Register(Grep{})

	// ── Filesystem — write ───────────────────────────────────
	r.Register(WriteFile{})
	r.Register(EditFile{})
	r.Register(MultiEdit{})
	r.Register(NotebookEdit{})

	// ── Shell ────────────────────────────────────────────────
	r.Register(Shell{})

	// ── Git ──────────────────────────────────────────────────
	r.Register(GitStatus{})
	r.Register(GitDiff{})
	r.Register(GitLog{})
	r.Register(GitCommit{})
	r.Register(GitBranch{})
	r.Register(EnterWorktree{})
	r.Register(ExitWorktree{})

	// ── Web ──────────────────────────────────────────────────
	r.Register(WebSearch{})
	r.Register(WebFetch{})

	// ── Agent ────────────────────────────────────────────────
	r.Register(DispatchAgent{})

	// ── Tasks ────────────────────────────────────────────────
	r.Register(CreateTask{})
	r.Register(UpdateTask{})
	r.Register(ListTasks{})
	r.Register(GetTask{})

	// ── Planning ─────────────────────────────────────────────
	r.Register(EnterPlanMode{})
	r.Register(ExitPlanMode{})

	// ── Configuration ────────────────────────────────────────
	r.Register(Config{})

	// ── User interaction ─────────────────────────────────────
	r.Register(AskUser{})

	// ── Memory ───────────────────────────────────────────────
	r.Register(SaveMemory{})
	r.Register(ReadMemory{})
}
