package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/codebase-foundation/cli/internal/tool"
)

// ──────────────────────────────────────────────────────────────
//  Agent event types (sent from agent goroutine → TUI)
// ──────────────────────────────────────────────────────────────

type EventType int

const (
	EventTextDelta  EventType = iota // streaming text chunk
	EventToolStart                   // tool execution starting
	EventToolResult                  // tool execution done
	EventUsage                       // token count update
	EventTurnStart                   // new agentic turn
	EventDone                        // agent finished all turns
	EventError                       // error occurred
	EventPermission                  // permission request for TUI
)

type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
}

type AgentEvent struct {
	Type    EventType
	Text    string         // EventTextDelta
	Tool    string         // EventToolStart / EventToolResult — tool name
	ToolID  string         // EventToolStart / EventToolResult — tool call ID
	Args    map[string]any // EventToolStart — parsed arguments
	Output     string             // EventToolResult — tool output
	Success    bool               // EventToolResult
	Tokens     TokenUsage         // EventUsage
	Turn       int                // EventTurnStart
	Error      error              // EventError
	Permission *PermissionRequest // EventPermission
}

// ──────────────────────────────────────────────────────────────
//  Agent
// ──────────────────────────────────────────────────────────────

const maxTurns = 30
const maxConsecutiveErrors = 5
const maxOutputTokensRecoveryLimit = 3

// systemPromptCore is the static portion of the system prompt.
// Tool descriptions are auto-generated from the registry at runtime —
// adding a tool automatically updates the prompt. Zero maintenance.
const systemPromptCore = `You are Codebase, an AI coding agent running in the user's terminal.
You have direct access to their filesystem, shell, and git. You help them build,
debug, and modify software projects.

# Using tools

Use the right tool for the job — don't reach for shell when a dedicated tool exists:
- To read files: use read_file, NOT shell with cat/head/tail
- To search code: use grep or search_files, NOT shell with grep/rg
- To find files: use glob or list_files, NOT shell with find/ls
- To edit files: use edit_file or multi_edit, NOT shell with sed/awk
- To create files: use write_file, NOT shell with echo/cat heredoc
- For git operations: use git_status/diff/log/commit/branch, NOT shell with git

Use shell for: builds, tests, package management, running programs, and anything that isn't covered by a dedicated tool.

IMPORTANT: Call multiple tools in a single response whenever possible. All read-only tools run in parallel automatically — the more you call at once, the faster you go:
- Need to understand a feature? Call read_file on 3-5 relevant files simultaneously, not one at a time.
- Need to find something? Call grep + glob + list_files in the same response.
- Need context? Call git_log + git_diff + read_file together.

Do NOT use dispatch_agent for simple research that you can do directly with parallel tool calls. dispatch_agent is for deep isolated investigations that need their own conversation context. For most questions, direct parallel reads are faster.

# Core principles

1. **Maximize parallelism** — call as many read-only tools as you need in a single response. They all run concurrently.
2. **Read before write** — ALWAYS read a file before editing it. Never guess at contents.
3. **Explore before assuming** — use glob/grep/list_files to understand project structure first.
4. **Minimal changes** — make targeted edits, don't rewrite entire files unless asked.
5. **Batch related edits** — use multi_edit for 2+ related changes across files.
6. **Verify your work** — after edits, run the build/tests if a command exists for them.

# Reversibility and safety

Consider the reversibility and blast radius of every action:
- **Freely do**: read files, search, list, run tests, create new files
- **Do with care**: edit existing files, run shell commands that modify state
- **Ask first**: delete files, force-push, drop databases, run destructive commands
- If you're unsure whether something is destructive, use ask_user to confirm

# Error recovery

- If edit_file fails with "old_text not found" → re-read the file, then retry with the correct text
- If edit_file fails with "found N times" → add more surrounding context to make old_text unique
- If shell fails → read the error carefully, diagnose the root cause, try a different approach
- Never repeat a failed tool call with identical arguments
- If you're stuck after 2-3 attempts, explain the problem to the user instead of looping

# Output style

- Be concise. Lead with the action, not the reasoning.
- Don't restate what the user said — just do it.
- When referencing code, include file_path:line_number for easy navigation.
- After completing work, briefly summarize what changed and why.
- Don't add features, refactor code, or "improve" things beyond what was asked.

# Task tracking

- For multi-step work (3+ steps), create tasks so the user can track progress
- Set status to "in_progress" BEFORE starting, "completed" only when fully done
- Keep subjects short and imperative (e.g. "Add auth middleware")
- For simple tasks, skip task creation — just do the work

# Planning

- For complex tasks, use enter_plan_mode to read and explore before coding
- Use ask_user when requirements are ambiguous or there are multiple valid approaches
- Use dispatch_agent for research questions that need isolated deep-dives`

type Agent struct {
	client      *LLMClient
	workDir     string
	history     []ChatMessage
	events      chan<- AgentEvent
	stopCh      <-chan struct{}
	files       int // count of files created/modified
	permCh      chan PermissionResponse
	permState   *PermissionState
	diag        *DiagnosticsEngine
	tasks       *TaskStore
	hooks       *HooksEngine
	fileHistory *FileHistory
	glue        *GlueClient
}

func NewAgent(client *LLMClient, workDir string, events chan<- AgentEvent, stopCh <-chan struct{}, tasks *TaskStore, glue *GlueClient) *Agent {
	sysContent := buildSystemPrompt(workDir)
	return &Agent{
		client:      client,
		workDir:     workDir,
		events:      events,
		stopCh:      stopCh,
		hooks:       NewHooksEngine(workDir),
		fileHistory: NewFileHistory(),
		glue:        glue,
		history: []ChatMessage{
			{Role: "system", Content: strPtr(sysContent)},
		},
		permCh:    make(chan PermissionResponse, 1),
		permState: &PermissionState{TrustedTools: map[string]bool{}},
		diag:      NewDiagnosticsEngine(workDir),
		tasks:     tasks,
	}
}

func strPtr(s string) *string { return &s }

// buildSystemPrompt assembles the system prompt with project context.
// Tool descriptions are auto-generated from the registry — adding a tool
// to RegisterAll() automatically includes it in the system prompt.
func buildSystemPrompt(workDir string) string {
	var sb strings.Builder
	sb.WriteString(systemPromptCore)

	// Auto-generate tool list from registry
	sb.WriteString("\n\n# Available tools\n\n")
	for _, t := range globalRegistry.All() {
		desc := t.Description()
		// Truncate very long descriptions for the system prompt
		if len(desc) > 200 {
			desc = desc[:197] + "..."
		}
		fmt.Fprintf(&sb, "- **%s**: %s\n", t.Name(), desc)
	}

	// Environment context
	sb.WriteString("\n\n## Environment\n\n")
	sb.WriteString(fmt.Sprintf("- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH))
	sb.WriteString(fmt.Sprintf("- Shell: %s\n", detectShellName()))
	sb.WriteString(fmt.Sprintf("- Date: %s\n", time.Now().Format("2006-01-02")))
	sb.WriteString(fmt.Sprintf("- Working directory: %s\n", workDir))

	// Git context
	if branch := getGitBranch(workDir); branch != "" {
		sb.WriteString(fmt.Sprintf("- Git branch: %s\n", branch))
	}

	// Platform-specific shell guidance
	if runtime.GOOS == "windows" {
		sb.WriteString("\nShell commands run in PowerShell. Use PowerShell syntax (e.g. `Get-ChildItem` not `ls`, `Remove-Item` not `rm`, `;` or `&&` to chain commands).\n")
	}

	// Inject cross-session memory
	injectMemoryContext(&sb, workDir)

	// Load project instructions if available
	projectInstructions := loadProjectInstructions(workDir)
	if projectInstructions != "" {
		sb.WriteString("\n## Project Instructions\n\n")
		sb.WriteString(projectInstructions)
		sb.WriteString("\n")
	}

	// Include top-level file tree
	tree := buildFileTree(workDir, 2)
	if tree != "" {
		sb.WriteString("\n## Project Structure\n\n```\n")
		sb.WriteString(tree)
		sb.WriteString("```\n")
	}

	return sb.String()
}

// detectShellName returns the name of the shell that will be used for commands.
func detectShellName() string {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("pwsh"); err == nil {
			return "pwsh (PowerShell Core)"
		}
		if _, err := exec.LookPath("powershell"); err == nil {
			return "powershell (Windows PowerShell)"
		}
		return "cmd.exe"
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		return "/bin/sh"
	}
	return filepath.Base(shell)
}

// getGitBranch returns the current git branch name, or empty if not in a git repo.
func getGitBranch(workDir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// loadProjectInstructions looks for project config files (AGENTS.md, CLAUDE.md,
// CODEX.md, .codebase) in the working directory and parent directories up to git root.
func loadProjectInstructions(workDir string) string {
	configFiles := []string{"AGENTS.md", "CLAUDE.md", "CODEX.md", ".codebase"}

	dir := workDir
	for {
		for _, name := range configFiles {
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err == nil && len(data) > 0 {
				content := string(data)
				// Cap at 20KB to avoid blowing up context
				if len(content) > 20*1024 {
					content = content[:20*1024] + "\n\n--- TRUNCATED (20KB limit) ---"
				}
				return content
			}
		}

		// Stop at git root or filesystem root
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return ""
}

// buildFileTree creates a simple tree listing of the project.
func buildFileTree(workDir string, maxDepth int) string {
	var sb strings.Builder
	buildTreeRecursive(&sb, workDir, workDir, "", maxDepth, 0)
	return sb.String()
}

func buildTreeRecursive(sb *strings.Builder, root, dir, prefix string, maxDepth, depth int) {
	if depth > maxDepth {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// Filter ignored directories
	var filtered []os.DirEntry
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && name != "." {
			continue
		}
		if e.IsDir() {
			if ignoreDirs[name] {
				continue
			}
		}
		filtered = append(filtered, e)
	}

	for i, e := range filtered {
		isLast := i == len(filtered)-1
		connector := "├── "
		childPrefix := prefix + "│   "
		if isLast {
			connector = "└── "
			childPrefix = prefix + "    "
		}

		if e.IsDir() {
			fmt.Fprintf(sb, "%s%s%s/\n", prefix, connector, e.Name())
			buildTreeRecursive(sb, root, filepath.Join(dir, e.Name()), childPrefix, maxDepth, depth+1)
		} else {
			fmt.Fprintf(sb, "%s%s%s\n", prefix, connector, e.Name())
		}
	}
}

// Run executes the agent loop for a user prompt. Blocks until done.
func (a *Agent) Run(prompt string) {
	a.history = append(a.history, ChatMessage{
		Role:    "user",
		Content: strPtr(prompt),
	})

	consecutiveErrors := 0
	outputTokenRecoveries := 0
	compactionFailures := 0

	for turn := 1; turn <= maxTurns; turn++ {
		// Check for stop signal
		select {
		case <-a.stopCh:
			a.events <- AgentEvent{Type: EventDone, Text: "Stopped by user."}
			return
		default:
		}

		// Proactive compaction — before the LLM call, not after it fails
		if needsCompaction(a.history, a.client.Model) {
			compacted, ok := compactHistory(a.client, a.history)
			if ok {
				a.history = compacted
				compactionFailures = 0
			} else {
				compactionFailures++
				// Circuit breaker: stop trying after 3 failures
				if compactionFailures >= 3 {
					a.events <- AgentEvent{Type: EventError,
						Error: fmt.Errorf("compaction failed %d times — context may be too large", compactionFailures)}
				}
			}
		}

		a.events <- AgentEvent{Type: EventTurnStart, Turn: turn}

		debugLog("turn %d: streaming LLM call (model=%s, protocol=%s, base=%s, history=%d msgs)",
			turn, a.client.Model, a.client.Protocol, a.client.BaseURL, len(a.history))

		// Stream LLM call
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			select {
			case <-a.stopCh:
				cancel()
			case <-ctx.Done():
			}
		}()
		streamCh := make(chan StreamEvent, 64)
		go a.client.StreamChat(ctx, a.history, allToolDefs(), streamCh)

		var textContent strings.Builder
		var toolCalls []ToolCall
		var lastUsage ChunkUsage
		var streamErr error

		// Streaming executor: starts tool execution as soon as tool calls
		// arrive, while the model may still be streaming more output.
		var executor *StreamingExecutor

		for evt := range streamCh {
			select {
			case <-a.stopCh:
				cancel()
				a.events <- AgentEvent{Type: EventDone, Text: "Stopped by user."}
				return
			default:
			}

			switch evt.Type {
			case StreamText:
				textContent.WriteString(evt.Text)
				a.events <- AgentEvent{Type: EventTextDelta, Text: evt.Text}

			case StreamToolCalls:
				toolCalls = evt.ToolCalls
				debugLog("stream: received %d tool calls", len(evt.ToolCalls))
				for _, tc := range evt.ToolCalls {
					debugLog("  tool_call: %s (id=%s)", tc.Function.Name, tc.ID)
				}
				// Start streaming execution immediately for read-only tools
				if executor == nil {
					executor = NewStreamingExecutor(a.toolEnv(), a.events)
				}
				for _, tc := range evt.ToolCalls {
					// Only auto-execute concurrency-safe tools during streaming.
					// Non-safe tools need permission checks — defer to batch execution.
					var argsMap map[string]any
					json.Unmarshal([]byte(tc.Function.Arguments), &argsMap)
					if registryIsParallelSafe(tc.Function.Name, argsMap) && !NeedsPermission(tc.Function.Name, argsMap) {
						executor.Submit(tc)
					}
				}

			case StreamUsage:
				lastUsage = evt.Usage
				a.events <- AgentEvent{
					Type:   EventUsage,
					Tokens: TokenUsage{PromptTokens: evt.Usage.PromptTokens, CompletionTokens: evt.Usage.CompletionTokens},
				}

			case StreamError:
				debugLog("stream: ERROR: %v", evt.Error)
				streamErr = evt.Error

			case StreamDone:
				// handled below
			}
		}
		cancel()

		// Wait for any streaming tool executions to complete
		if executor != nil {
			executor.Wait()
		}

		_ = lastUsage

		// ── Error recovery ──────────────────────────────────
		if streamErr != nil {
			errMsg := streamErr.Error()
			recovered := false

			// Recovery: prompt too long → reactive compact
			if isContextTooLongError(errMsg) {
				a.events <- AgentEvent{Type: EventError,
					Error: fmt.Errorf("context too long — attempting compaction")}

				compacted, ok := compactHistory(a.client, a.history)
				if ok {
					a.history = compacted
					a.events <- AgentEvent{Type: EventError,
						Error: fmt.Errorf("compacted context, retrying")}
					recovered = true
				}
			}

			// Recovery: max output tokens → inject resume message
			if isMaxOutputTokensError(errMsg) && outputTokenRecoveries < maxOutputTokensRecoveryLimit {
				outputTokenRecoveries++
				// Save what we got so far
				if textContent.Len() > 0 {
					a.history = append(a.history, ChatMessage{
						Role:    "assistant",
						Content: strPtr(textContent.String()),
					})
				}
				// Inject recovery message
				a.history = append(a.history, ChatMessage{
					Role:    "user",
					Content: strPtr("Output limit reached. Resume directly — no apology, no recap. Pick up exactly where you left off. If you were mid-code, continue the code. Break remaining work into smaller pieces."),
				})
				a.events <- AgentEvent{Type: EventError,
					Error: fmt.Errorf("output limit hit, resuming (attempt %d/%d)", outputTokenRecoveries, maxOutputTokensRecoveryLimit)}
				recovered = true
			}

			// Recovery: rate limit → wait and retry
			if isRateLimitError(errMsg) {
				backoff := time.Duration(1<<uint(min(consecutiveErrors, 4))) * time.Second
				a.events <- AgentEvent{Type: EventError,
					Error: fmt.Errorf("rate limited — waiting %s before retry", backoff)}
				time.Sleep(backoff)
				recovered = true
			}

			if recovered {
				continue // retry this turn
			}

			// Unrecoverable error
			a.events <- AgentEvent{Type: EventError, Error: fmt.Errorf("%s", humanizeError(streamErr))}
			a.events <- AgentEvent{Type: EventDone, Text: "Error occurred."}
			return
		}

		// Reset recovery counter on successful response
		outputTokenRecoveries = 0

		// ── Process response ────────────────────────────────

		assistantMsg := ChatMessage{Role: "assistant"}
		txt := textContent.String()
		if txt != "" {
			assistantMsg.Content = strPtr(txt)
		}
		if len(toolCalls) > 0 {
			assistantMsg.ToolCalls = toolCalls
		}
		a.history = append(a.history, assistantMsg)

		// If no tool calls, we're done
		if len(toolCalls) == 0 {
			a.events <- AgentEvent{Type: EventDone, Text: txt}
			return
		}

		// Collect results from streaming executor (tools already executed)
		executedIDs := make(map[string]bool)
		if executor != nil {
			for _, r := range executor.Results() {
				executedIDs[r.tc.ID] = true
				a.history = append(a.history, ChatMessage{
					Role: "tool", ToolCallID: r.tc.ID, Name: r.tc.Function.Name, Content: strPtr(r.output),
				})
			}
			if executor.HasErrors() {
				consecutiveErrors++
			} else if len(executor.Results()) > 0 {
				consecutiveErrors = 0
			}
		}

		// Execute remaining tool calls (those not handled by streaming executor)
		var remaining []ToolCall
		for _, tc := range toolCalls {
			if !executedIDs[tc.ID] {
				remaining = append(remaining, tc)
			}
		}
		if len(remaining) > 0 {
			a.executeToolCalls(remaining, &consecutiveErrors)
		}

		if consecutiveErrors >= maxConsecutiveErrors {
			a.events <- AgentEvent{
				Type:  EventError,
				Error: fmt.Errorf("too many consecutive tool errors (%d), stopping", consecutiveErrors),
			}
			a.events <- AgentEvent{Type: EventDone, Text: "Too many errors."}
			return
		}
	}

	a.events <- AgentEvent{Type: EventDone, Text: fmt.Sprintf("Reached maximum turns (%d). You can continue with a follow-up prompt.", maxTurns)}
}

// ── Error classification helpers ────────────────────────────

func isContextTooLongError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "context_length_exceeded") ||
		strings.Contains(lower, "prompt is too long") ||
		strings.Contains(lower, "maximum context length") ||
		strings.Contains(lower, "prompt too long") ||
		strings.Contains(lower, "request too large")
}

func isMaxOutputTokensError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "max_tokens") ||
		strings.Contains(lower, "maximum output") ||
		strings.Contains(lower, "length limit") ||
		strings.Contains(lower, "output token")
}

func isRateLimitError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "rate_limit") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "429") ||
		strings.Contains(lower, "too many requests")
}

// ──────────────────────────────────────────────────────────────
//  Batch-partitioned tool execution
//
//  Consecutive concurrency-safe tools are grouped into parallel batches.
//  Non-safe tools run serially, one at a time. This preserves ordering:
//
//    [read, read, WRITE, read, WRITE]
//     → batch1(parallel: read, read)
//     → batch2(serial: WRITE)
//     → batch3(parallel: read)    ← sees WRITE's effects
//     → batch4(serial: WRITE)
//
//  Concurrency safety is INPUT-DEPENDENT: the same tool can be safe
//  for some args and unsafe for others (e.g. shell("ls") vs shell("rm")).
// ──────────────────────────────────────────────────────────────

type toolBatch struct {
	parallel bool       // true = run all in parallel, false = run one at a time
	calls    []ToolCall
}

// partitionToolCalls groups consecutive concurrency-safe tools into parallel
// batches and non-safe tools into serial batches.
func (a *Agent) partitionToolCalls(toolCalls []ToolCall) []toolBatch {
	var batches []toolBatch

	for _, tc := range toolCalls {
		var argsMap map[string]any
		json.Unmarshal([]byte(tc.Function.Arguments), &argsMap)

		safe := registryIsParallelSafe(tc.Function.Name, argsMap)

		if safe && len(batches) > 0 && batches[len(batches)-1].parallel {
			// Extend existing parallel batch
			batches[len(batches)-1].calls = append(batches[len(batches)-1].calls, tc)
		} else {
			batches = append(batches, toolBatch{parallel: safe, calls: []ToolCall{tc}})
		}
	}
	return batches
}

// executeToolCalls partitions tool calls into batches and executes them
// with proper ordering: parallel batches run concurrently, serial batches
// run one tool at a time.
func (a *Agent) executeToolCalls(toolCalls []ToolCall, consecutiveErrors *int) {
	batches := a.partitionToolCalls(toolCalls)
	allErrors := true

	for _, batch := range batches {
		if batch.parallel {
			a.executeBatchParallel(batch.calls, &allErrors)
		} else {
			a.executeBatchSerial(batch.calls, &allErrors)
		}
	}

	if allErrors {
		*consecutiveErrors++
	} else {
		*consecutiveErrors = 0
	}
}

// executeBatchParallel runs all tools in the batch concurrently.
func (a *Agent) executeBatchParallel(calls []ToolCall, allErrors *bool) {
	type result struct {
		tc      ToolCall
		args    map[string]any
		output  string
		success bool
	}
	results := make([]result, len(calls))
	var wg sync.WaitGroup

	for i, tc := range calls {
		var argsMap map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &argsMap); err != nil {
			argsMap = map[string]any{"_raw": tc.Function.Arguments}
		}

		a.events <- AgentEvent{
			Type:   EventToolStart,
			Tool:   tc.Function.Name,
			ToolID: tc.ID,
			Args:   argsMap,
		}

		wg.Add(1)
		go func(idx int, tc ToolCall, args map[string]any) {
			defer wg.Done()
			output, success := a.dispatchTool(tc, args)
			results[idx] = result{tc: tc, args: args, output: output, success: success}
		}(i, tc, argsMap)
	}

	wg.Wait()

	for _, r := range results {
		if r.success {
			*allErrors = false
		}
		a.events <- AgentEvent{
			Type:    EventToolResult,
			Tool:    r.tc.Function.Name,
			ToolID:  r.tc.ID,
			Args:    r.args,
			Output:  r.output,
			Success: r.success,
		}
		a.history = append(a.history, ChatMessage{
			Role:       "tool",
			ToolCallID: r.tc.ID,
			Name:       r.tc.Function.Name,
			Content:    strPtr(r.output),
		})
	}
}

// executeBatchSerial runs tools one at a time with permission checks.
func (a *Agent) executeBatchSerial(calls []ToolCall, allErrors *bool) {
	for _, tc := range calls {
		var argsMap map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &argsMap); err != nil {
			argsMap = map[string]any{"_raw": tc.Function.Arguments}
		}

		// Run PreToolUse hooks — can block execution
		if a.hooks != nil {
			hookResult := a.hooks.Run(HookPreToolUse, HookInput{
				ToolName:  tc.Function.Name,
				ToolInput: argsMap,
			})
			if hookResult != nil && hookResult.Blocked {
				output := fmt.Sprintf("Blocked by hook: %s", hookResult.Message)
				a.events <- AgentEvent{Type: EventToolStart, Tool: tc.Function.Name, ToolID: tc.ID, Args: argsMap}
				a.events <- AgentEvent{Type: EventToolResult, Tool: tc.Function.Name, ToolID: tc.ID, Args: argsMap, Output: output, Success: false}
				a.history = append(a.history, ChatMessage{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: strPtr(output)})
				continue
			}
		}

		// Check permission before executing
		if !a.checkPermission(tc.Function.Name, argsMap) {
			output := "Skipped: permission denied by user"
			a.events <- AgentEvent{
				Type:   EventToolStart,
				Tool:   tc.Function.Name,
				ToolID: tc.ID,
				Args:   argsMap,
			}
			a.events <- AgentEvent{
				Type:    EventToolResult,
				Tool:    tc.Function.Name,
				ToolID:  tc.ID,
				Args:    argsMap,
				Output:  output,
				Success: false,
			}
			a.history = append(a.history, ChatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    strPtr(output),
			})
			continue
		}

		a.events <- AgentEvent{
			Type:   EventToolStart,
			Tool:   tc.Function.Name,
			ToolID: tc.ID,
			Args:   argsMap,
		}

		output, success := a.dispatchTool(tc, argsMap)

		if success {
			*allErrors = false
			isFileEdit := tc.Function.Name == "write_file" || tc.Function.Name == "edit_file" || tc.Function.Name == "multi_edit"
			if isFileEdit {
				a.files++
				a.maybeInjectDiagnostics(tc.Function.Name, argsMap)

				// Run PostEdit hooks (e.g., auto-lint, auto-format)
				if a.hooks != nil {
					var editedFiles []string
					if p, ok := argsMap["path"].(string); ok {
						editedFiles = []string{p}
					}
					a.hooks.Run(HookPostEdit, HookInput{
						ToolName:  tc.Function.Name,
						ToolInput: argsMap,
						Files:     editedFiles,
						Output:    output,
					})
				}
			}
		}

		// Run PostToolUse hooks
		if a.hooks != nil {
			a.hooks.Run(HookPostToolUse, HookInput{
				ToolName:  tc.Function.Name,
				ToolInput: argsMap,
				Output:    output,
			})
		}

		a.events <- AgentEvent{
			Type:    EventToolResult,
			Tool:    tc.Function.Name,
			ToolID:  tc.ID,
			Args:    argsMap,
			Output:  output,
			Success: success,
		}

		a.history = append(a.history, ChatMessage{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    strPtr(output),
		})
	}
}

// dispatchTool routes a tool call through the registry.
// All tools go through the same path — no special cases.
// Large outputs are automatically persisted to disk.
func (a *Agent) dispatchTool(tc ToolCall, args map[string]any) (string, bool) {
	debugLog("tool: %s (id=%s, args=%d bytes)", tc.Function.Name, tc.ID, len(tc.Function.Arguments))
	result := registryExecute(context.Background(), tc.Function.Name, args, a.toolEnv())
	output := maybePersistToolResult(tc.Function.Name, result.Output)
	debugLog("tool: %s → success=%v, output=%d bytes", tc.Function.Name, result.Success, len(output))
	return output, result.Success
}

// FilesChanged returns how many files the agent has created/modified.
func (a *Agent) FilesChanged() int {
	return a.files
}

// toolEnv constructs the Env passed to tools in the registry.
func (a *Agent) toolEnv() *tool.Env {
	return &tool.Env{
		WorkDir:  a.workDir,
		Turn:     0, // turn tracking is in chatModel, not agent
		Subagent: &agentSubagentRunner{client: a.client, workDir: a.workDir},
		Tasks:    a.tasks,
		History:  a.fileHistory,
		// Glue: wired when GlueClient implements tool.GlueRunner
	}
}

// agentSubagentRunner adapts RunSubagentWithConfig to the tool.SubagentRunner interface.
type agentSubagentRunner struct {
	client  *LLMClient
	workDir string
}

func (r *agentSubagentRunner) RunSubagent(task string) (string, error) {
	// Parse enriched task format: __agent_config__:{json}\n{actual task}
	cfg := SubagentConfig{
		Task:      task,
		AgentType: "explore",
		Depth:     1,
	}

	if strings.HasPrefix(task, "__agent_config__:") {
		parts := strings.SplitN(task, "\n", 2)
		configStr := strings.TrimPrefix(parts[0], "__agent_config__:")
		var config map[string]string
		if err := json.Unmarshal([]byte(configStr), &config); err == nil {
			if v := config["type"]; v != "" {
				cfg.AgentType = v
			}
			if v := config["isolation"]; v != "" {
				cfg.Isolation = v
			}
			if v := config["model"]; v != "" {
				cfg.Model = v
			}
		}
		if len(parts) > 1 {
			cfg.Task = parts[1]
		}
	}

	return RunSubagentWithConfig(r.client, r.workDir, cfg)
}

// maybeInjectDiagnostics runs language checkers after file modifications
// and injects a system message with errors if found.
func (a *Agent) maybeInjectDiagnostics(toolName string, args map[string]any) {
	if a.diag == nil || !a.diag.Enabled {
		return
	}

	// Determine which files were modified
	var files []string
	switch toolName {
	case "write_file", "edit_file":
		if p, ok := args["path"].(string); ok {
			files = []string{p}
		}
	case "multi_edit":
		if edits, ok := args["edits"]; ok {
			if arr, ok := edits.([]interface{}); ok {
				seen := map[string]bool{}
				for _, e := range arr {
					if m, ok := e.(map[string]interface{}); ok {
						if p, ok := m["path"].(string); ok && !seen[p] {
							files = append(files, p)
							seen[p] = true
						}
					}
				}
			}
		}
	}

	if len(files) == 0 {
		return
	}

	diags := a.diag.CheckFiles(files)
	if len(diags) == 0 {
		return
	}

	// Inject as system message so the LLM sees errors
	msg := formatDiagnosticsMessage(diags)
	a.history = append(a.history, ChatMessage{
		Role:    "system",
		Content: strPtr(msg),
	})
}

// checkPermission asks the TUI for permission if needed.
// Returns true if the tool should execute, false to skip.
func (a *Agent) checkPermission(toolName string, args map[string]any) bool {
	// Check session-level trust
	if a.permState.Level == PermTrustAll {
		return true
	}
	if a.permState.TrustedTools[toolName] {
		return true
	}

	// Check if this tool needs permission
	if !NeedsPermission(toolName, args) {
		return true
	}

	// Build permission request with glue-powered explanation
	req := &PermissionRequest{
		Tool:    toolName,
		Args:    args,
		Summary: PermissionSummary(toolName, args),
	}

	// Use glue to explain the action and assess risk (non-blocking, best-effort)
	if a.glue != nil {
		if expl := a.glue.ExplainPermission(toolName, args); expl != nil {
			req.Risk = expl.Risk
			req.Explanation = expl.Explanation
		}
	}

	a.events <- AgentEvent{Type: EventPermission, Permission: req}

	// Block waiting for TUI response (or stop signal)
	select {
	case resp := <-a.permCh:
		if resp.TrustLevel == PermTrustTool {
			a.permState.TrustedTools[toolName] = true
		} else if resp.TrustLevel == PermTrustAll {
			a.permState.Level = PermTrustAll
		}
		return resp.Allowed
	case <-a.stopCh:
		return false
	}
}
