package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────
//  Hooks system — automate behaviors triggered by events
//
//  Users configure hooks in ~/.codebase/hooks.json or
//  .codebase/hooks.json (project-level). Hooks run shell
//  commands triggered by events like tool use, file edits,
//  session start, etc.
//
//  Better than CC: simpler config (one file), focused events
//  (6 vs 26), PostEdit hook for auto-lint/test, and glue
//  model integration for smart hook decisions.
// ──────────────────────────────────────────────────────────────

// HookEvent is the event type that triggers hooks.
type HookEvent string

const (
	HookPreToolUse      HookEvent = "PreToolUse"       // before tool execution
	HookPostToolUse     HookEvent = "PostToolUse"       // after tool execution
	HookPostEdit        HookEvent = "PostEdit"          // after file write/edit (for lint/test)
	HookUserPrompt      HookEvent = "UserPromptSubmit"  // when user sends a message
	HookSessionStart    HookEvent = "SessionStart"      // session begins
	HookStop            HookEvent = "Stop"              // before agent concludes response
)

// HookConfig defines a single hook.
type HookConfig struct {
	Event   HookEvent `json:"event"`             // which event triggers this hook
	Matcher string    `json:"matcher,omitempty"`  // tool name or pattern to match (for tool events)
	Command string    `json:"command"`            // shell command to execute
	Timeout int       `json:"timeout,omitempty"`  // timeout in seconds (default 30)
	Async   bool      `json:"async,omitempty"`    // run in background without blocking
}

// HookInput is the JSON sent to the hook's stdin.
type HookInput struct {
	Event     HookEvent      `json:"hook_event"`
	ToolName  string         `json:"tool_name,omitempty"`
	ToolInput map[string]any `json:"tool_input,omitempty"`
	Output    string         `json:"tool_output,omitempty"`
	Files     []string       `json:"files,omitempty"`
	Prompt    string         `json:"prompt,omitempty"`
	WorkDir   string         `json:"cwd"`
}

// HookResult is what a hook returns.
type HookResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Blocked  bool   // exit code 2 = block the action
	Message  string // stderr content if blocked
}

// HooksEngine loads and executes hooks.
type HooksEngine struct {
	hooks   []HookConfig
	workDir string
}

// NewHooksEngine loads hooks from config files.
func NewHooksEngine(workDir string) *HooksEngine {
	engine := &HooksEngine{workDir: workDir}
	engine.loadHooks()
	return engine
}

// loadHooks reads hooks from ~/.codebase/hooks.json and .codebase/hooks.json
func (e *HooksEngine) loadHooks() {
	// Project-level hooks (higher priority)
	e.loadFile(filepath.Join(e.workDir, ".codebase", "hooks.json"))

	// User-level hooks
	home, err := os.UserHomeDir()
	if err == nil {
		e.loadFile(filepath.Join(home, ".codebase", "hooks.json"))
	}
}

func (e *HooksEngine) loadFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	// Support both array format and object-with-hooks format
	var hooks []HookConfig
	if err := json.Unmarshal(data, &hooks); err != nil {
		// Try {"hooks": [...]} format
		var wrapper struct {
			Hooks []HookConfig `json:"hooks"`
		}
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return
		}
		hooks = wrapper.Hooks
	}

	e.hooks = append(e.hooks, hooks...)
}

// Run executes all matching hooks for an event.
// Returns the first blocking result, or nil if all hooks passed.
func (e *HooksEngine) Run(event HookEvent, input HookInput) *HookResult {
	input.Event = event
	input.WorkDir = e.workDir

	for _, hook := range e.hooks {
		if hook.Event != event {
			continue
		}

		// Match tool name if matcher specified
		if hook.Matcher != "" && input.ToolName != "" {
			if !matchHook(hook.Matcher, input.ToolName) {
				continue
			}
		}

		result := e.executeHook(hook, input)
		if result.Blocked {
			return &result
		}
	}
	return nil
}

// HasHooks returns true if any hooks are configured for the event.
func (e *HooksEngine) HasHooks(event HookEvent) bool {
	for _, h := range e.hooks {
		if h.Event == event {
			return true
		}
	}
	return false
}

// matchHook checks if a tool name matches a hook's matcher pattern.
// Supports: exact match, pipe-separated alternatives, glob patterns.
func matchHook(matcher, toolName string) bool {
	// Pipe-separated alternatives: "write_file|edit_file|multi_edit"
	if strings.Contains(matcher, "|") {
		for _, alt := range strings.Split(matcher, "|") {
			if strings.TrimSpace(alt) == toolName {
				return true
			}
		}
		return false
	}

	// Glob pattern
	if strings.Contains(matcher, "*") || strings.Contains(matcher, "?") {
		matched, _ := filepath.Match(matcher, toolName)
		return matched
	}

	// Exact match
	return matcher == toolName
}

// executeHook runs a single hook command.
func (e *HooksEngine) executeHook(hook HookConfig, input HookInput) HookResult {
	timeout := hook.Timeout
	if timeout <= 0 {
		timeout = 30
	}

	// Serialize input to JSON for stdin
	inputJSON, _ := json.Marshal(input)

	// Build command
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell, "-c", hook.Command)
	cmd.Dir = e.workDir
	cmd.Stdin = strings.NewReader(string(inputJSON) + "\n")
	cmd.Env = append(os.Environ(),
		"CODEBASE_PROJECT_DIR="+e.workDir,
		"CODEBASE_HOOK_EVENT="+string(hook.Event),
	)

	if hook.Async {
		// Fire and forget
		cmd.Start()
		return HookResult{ExitCode: 0}
	}

	// Run with timeout
	done := make(chan struct{})
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	go func() {
		cmd.Run()
		close(done)
	}()

	select {
	case <-done:
		exitCode := 0
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		return HookResult{
			ExitCode: exitCode,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			Blocked:  exitCode == 2,
			Message:  strings.TrimSpace(stderr.String()),
		}
	case <-time.After(time.Duration(timeout) * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return HookResult{
			ExitCode: -1,
			Stderr:   fmt.Sprintf("Hook timed out after %ds", timeout),
		}
	}
}

// Count returns the number of configured hooks.
func (e *HooksEngine) Count() int {
	return len(e.hooks)
}
