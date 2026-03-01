package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
)

type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
}

type AgentEvent struct {
	Type    EventType
	Text    string         // EventTextDelta
	Tool    string         // EventToolStart / EventToolResult — tool name
	Args    map[string]any // EventToolStart — parsed arguments
	Output  string         // EventToolResult — tool output
	Success bool           // EventToolResult
	Tokens  TokenUsage     // EventUsage
	Turn    int            // EventTurnStart
	Error   error          // EventError
}

// ──────────────────────────────────────────────────────────────
//  Agent
// ──────────────────────────────────────────────────────────────

const maxTurns = 30
const maxConsecutiveErrors = 5

const systemPrompt = `You are Codebase, a local AI coding agent running in the user's terminal.
You have direct access to their filesystem and shell. You help them build,
debug, and modify software projects.

Available tools:
- read_file: Read file contents with line numbers. Use offset/limit for large files.
- write_file: Create or overwrite a file. Parent directories are created automatically.
- edit_file: Surgical find-and-replace in a file. old_text must match exactly and be unique.
- multi_edit: Batch multiple edits across files. Per-file atomicity with rollback.
- list_files: List directory contents or glob for files (e.g. "**/*.go").
- search_files: Regex search across files (powered by ripgrep). Find definitions, usages, etc.
- web_search: Search the web. Use for current info, docs, versions, error solutions, or anything not in local files.
- dispatch_agent: Spawn a read-only research subagent to investigate questions in isolated context.
- shell: Run any shell command. Use for builds, tests, git, package management.

Guidelines:
- You can call multiple tools in parallel — read_file, list_files, search_files, web_search, and dispatch_agent all run concurrently
- Use list_files and search_files to explore the project before making changes
- Read files before editing them — understand existing code first
- Make targeted, minimal changes — don't rewrite entire files unnecessarily
- For multiple related edits, prefer multi_edit over separate edit_file calls
- If a tool fails, read the error and try a different approach
- When finished, briefly summarize what you changed and why`

type Agent struct {
	client   *LLMClient
	workDir  string
	history  []ChatMessage
	events   chan<- AgentEvent
	stopCh   <-chan struct{}
	files    int // count of files created/modified
}

func NewAgent(client *LLMClient, workDir string, events chan<- AgentEvent, stopCh <-chan struct{}) *Agent {
	sysContent := buildSystemPrompt(workDir)
	return &Agent{
		client:  client,
		workDir: workDir,
		events:  events,
		stopCh:  stopCh,
		history: []ChatMessage{
			{Role: "system", Content: strPtr(sysContent)},
		},
	}
}

func strPtr(s string) *string { return &s }

// buildSystemPrompt assembles the system prompt with project context.
func buildSystemPrompt(workDir string) string {
	var sb strings.Builder
	sb.WriteString(systemPrompt)
	sb.WriteString(fmt.Sprintf("\n\nWorking directory: %s\n", workDir))

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

	for turn := 1; turn <= maxTurns; turn++ {
		// Check for stop signal
		select {
		case <-a.stopCh:
			a.events <- AgentEvent{Type: EventDone, Text: "Stopped by user."}
			return
		default:
		}

		// Check if compaction is needed before the LLM call
		if needsCompaction(a.history, a.client.Model) {
			compacted, ok := compactHistory(a.client, a.history)
			if ok {
				a.history = compacted
			}
		}

		a.events <- AgentEvent{Type: EventTurnStart, Turn: turn}

		// Stream LLM call
		streamCh := make(chan StreamEvent, 64)
		go a.client.StreamChat(a.history, toolDefs, streamCh)

		var textContent strings.Builder
		var toolCalls []ToolCall
		var lastUsage ChunkUsage

		for evt := range streamCh {
			// Check stop between stream events
			select {
			case <-a.stopCh:
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

			case StreamUsage:
				lastUsage = evt.Usage
				a.events <- AgentEvent{
					Type:   EventUsage,
					Tokens: TokenUsage{PromptTokens: evt.Usage.PromptTokens, CompletionTokens: evt.Usage.CompletionTokens},
				}

			case StreamError:
				a.events <- AgentEvent{Type: EventError, Error: evt.Error}
				a.events <- AgentEvent{Type: EventDone, Text: "Error occurred."}
				return

			case StreamDone:
				// handled below
			}
		}

		_ = lastUsage

		// Build assistant message for history
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

		// Execute tool calls — parallel for read-only, sequential for mutations
		a.executeToolCalls(toolCalls, &consecutiveErrors)

		if consecutiveErrors >= maxConsecutiveErrors {
			a.events <- AgentEvent{
				Type: EventError,
				Error: fmt.Errorf("too many consecutive tool errors (%d), stopping", consecutiveErrors),
			}
			a.events <- AgentEvent{Type: EventDone, Text: "Too many errors."}
			return
		}

		// Loop back for next turn
	}

	a.events <- AgentEvent{Type: EventDone, Text: "Reached maximum turns."}
}

// executeToolCalls runs tool calls with parallel execution for read-only tools.
func (a *Agent) executeToolCalls(toolCalls []ToolCall, consecutiveErrors *int) {
	// Classify tools
	var parallel []ToolCall
	var sequential []ToolCall
	for _, tc := range toolCalls {
		if IsParallelSafe(tc.Function.Name) {
			parallel = append(parallel, tc)
		} else {
			sequential = append(sequential, tc)
		}
	}

	allErrors := true

	// Run read-only tools in parallel
	if len(parallel) > 0 {
		type result struct {
			tc      ToolCall
			args    map[string]any
			output  string
			success bool
		}
		results := make([]result, len(parallel))
		var wg sync.WaitGroup

		for i, tc := range parallel {
			var argsMap map[string]any
			json.Unmarshal([]byte(tc.Function.Arguments), &argsMap)

			a.events <- AgentEvent{
				Type: EventToolStart,
				Tool: tc.Function.Name,
				Args: argsMap,
			}

			wg.Add(1)
			go func(idx int, tc ToolCall, args map[string]any) {
				defer wg.Done()
				var output string
				var success bool
				if tc.Function.Name == "dispatch_agent" {
					task := ""
					if args != nil {
						task, _ = args["task"].(string)
					}
					res, err := RunSubagent(a.client, a.workDir, task)
					if err != nil {
						output = fmt.Sprintf("Subagent error: %v", err)
						success = false
					} else {
						output = res
						success = true
					}
				} else {
					output, success = ExecuteTool(tc.Function.Name, tc.Function.Arguments, a.workDir)
				}
				results[idx] = result{tc: tc, args: args, output: output, success: success}
			}(i, tc, argsMap)
		}

		wg.Wait()

		for _, r := range results {
			if r.success {
				allErrors = false
			}

			a.events <- AgentEvent{
				Type:    EventToolResult,
				Tool:    r.tc.Function.Name,
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

	// Run mutating tools sequentially
	for _, tc := range sequential {
		var argsMap map[string]any
		json.Unmarshal([]byte(tc.Function.Arguments), &argsMap)

		a.events <- AgentEvent{
			Type: EventToolStart,
			Tool: tc.Function.Name,
			Args: argsMap,
		}

		var output string
		var success bool
		if tc.Function.Name == "dispatch_agent" {
			task := ""
			if argsMap != nil {
				task, _ = argsMap["task"].(string)
			}
			res, err := RunSubagent(a.client, a.workDir, task)
			if err != nil {
				output = fmt.Sprintf("Subagent error: %v", err)
				success = false
			} else {
				output = res
				success = true
			}
		} else {
			output, success = ExecuteTool(tc.Function.Name, tc.Function.Arguments, a.workDir)
		}

		if success {
			allErrors = false
			if tc.Function.Name == "write_file" || tc.Function.Name == "edit_file" || tc.Function.Name == "multi_edit" {
				a.files++
			}
		}

		a.events <- AgentEvent{
			Type:    EventToolResult,
			Tool:    tc.Function.Name,
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

	if allErrors {
		*consecutiveErrors++
	} else {
		*consecutiveErrors = 0
	}
}

// FilesChanged returns how many files the agent has created/modified.
func (a *Agent) FilesChanged() int {
	return a.files
}
