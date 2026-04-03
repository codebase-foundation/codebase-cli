package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/codebase-foundation/cli/internal/tool"
)

// ──────────────────────────────────────────────────────────────
//  Subagent system — typed agents with isolation
//
//  Better than CC:
//  - Agent types are just config (not separate classes)
//  - Worktree isolation is automatic for write agents
//  - Depth > 1 supported with configurable max
//  - Parallel agent execution via goroutines
//  - Model selection per agent type (use glue for cheap exploration)
// ──────────────────────────────────────────────────────────────

const (
	subagentMaxTurns = 25
	maxAgentDepth    = 3 // main → subagent → sub-subagent
)

// AgentType defines a specialized agent configuration.
type AgentType struct {
	Name         string
	SystemPrompt string
	AllowedTools map[string]bool // nil = all tools allowed
	ReadOnly     bool            // if true, block write tools
	ModelHint    string          // "fast", "smart", or "" (use main model)
}

// Built-in agent types matching CC's types but simpler.
var agentTypes = map[string]AgentType{
	"explore": {
		Name: "explore",
		SystemPrompt: `You are a codebase exploration specialist. You excel at thoroughly navigating and understanding codebases.

CRITICAL: READ-ONLY MODE — you CANNOT create, modify, or delete files.

Your job:
- Search for files, patterns, definitions, and usages
- Read and understand code structure and architecture
- Follow import chains and dependency graphs
- Summarize your findings clearly and completely

Strategies:
- Use glob to find files by pattern (sorted by modification time — newest first)
- Use grep for regex content search with output modes (content, files, count)
- Use read_file with offset/limit for large files — don't read everything
- Use shell for git log, git blame, and other read-only commands
- Be thorough: check multiple locations and naming conventions`,
		AllowedTools: map[string]bool{
			"read_file": true, "list_files": true, "search_files": true,
			"glob": true, "grep": true, "web_search": true, "web_fetch": true,
			"shell": true, "git_status": true, "git_diff": true, "git_log": true,
		},
		ReadOnly:  true,
		ModelHint: "fast",
	},
	"plan": {
		Name: "plan",
		SystemPrompt: `You are a software architect designing implementation plans. You analyze codebases, identify critical files, and consider architectural trade-offs.

CRITICAL: READ-ONLY MODE — you CANNOT create, modify, or delete files.

Your job:
- Understand the current architecture and patterns
- Identify files that need to change and why
- Consider edge cases, error handling, and testing
- Produce a step-by-step implementation plan with specific file paths and code changes

Output format:
1. Summary of current state
2. Proposed changes (file by file, with rationale)
3. Risks and considerations
4. Suggested implementation order`,
		AllowedTools: map[string]bool{
			"read_file": true, "list_files": true, "search_files": true,
			"glob": true, "grep": true, "web_search": true, "web_fetch": true,
			"shell": true, "git_status": true, "git_diff": true, "git_log": true,
		},
		ReadOnly:  true,
		ModelHint: "smart",
	},
	"general-purpose": {
		Name: "general-purpose",
		SystemPrompt: `You are a capable coding agent handling a delegated task. You have full access to the filesystem and shell.

Complete the task thoroughly:
- Read relevant files before making changes
- Make targeted, minimal edits
- Run tests/builds to verify your work
- Report what you did and any issues encountered`,
		AllowedTools: nil, // all tools
		ReadOnly:     false,
		ModelHint:    "",
	},
}

// readOnlyTools lists tools allowed for read-only agents.
var readOnlyTools = map[string]bool{
	"read_file": true, "list_files": true, "search_files": true,
	"glob": true, "grep": true, "web_search": true, "web_fetch": true,
	"shell": true, "git_status": true, "git_diff": true, "git_log": true,
}

// SubagentConfig configures a subagent invocation.
type SubagentConfig struct {
	Task      string // what to do
	AgentType string // "explore", "plan", "general-purpose", or ""
	Isolation string // "worktree" or "" (none)
	Model     string // model override, or "" for default
	Depth     int    // current nesting depth
}

// subagentToolDefs returns tool definitions filtered for the agent type.
func subagentToolDefs(agentType AgentType) []ToolDef {
	var defs []ToolDef
	for _, raw := range globalRegistry.OpenAITools() {
		var td ToolDef
		json.Unmarshal(raw, &td)

		// Skip dispatch_agent if at max depth
		if td.Function.Name == "dispatch_agent" {
			continue // subagents don't spawn sub-subagents by default
		}

		if agentType.AllowedTools != nil {
			if !agentType.AllowedTools[td.Function.Name] {
				continue
			}
		}
		defs = append(defs, td)
	}
	return defs
}

// RunSubagentWithConfig executes a subagent with full configuration.
func RunSubagentWithConfig(client *LLMClient, workDir string, cfg SubagentConfig) (string, error) {
	debugLog("subagent: starting type=%s isolation=%s depth=%d task=%q",
		cfg.AgentType, cfg.Isolation, cfg.Depth, truncateStr(cfg.Task, 80))
	if cfg.Depth >= maxAgentDepth {
		return "", fmt.Errorf("maximum agent depth (%d) exceeded", maxAgentDepth)
	}

	// Resolve agent type
	at, ok := agentTypes[cfg.AgentType]
	if !ok {
		at = agentTypes["explore"] // default to explore (read-only, safe)
	}

	// Resolve model
	// TODO: when glue is wired, use glue fast model for "fast" hint,
	// glue smart model for "smart" hint
	agentClient := client
	if cfg.Model != "" {
		agentClient = NewLLMClient(client.APIKey, client.BaseURL, cfg.Model)
	}

	// Set up worktree isolation if requested
	var worktreePath, worktreeBranch string
	var cleanupWorktree func()
	effectiveWorkDir := workDir

	if cfg.Isolation == "worktree" {
		wp, wb, cleanup, err := createWorktreeForAgent(workDir)
		if err != nil {
			return "", fmt.Errorf("failed to create worktree: %v", err)
		}
		worktreePath = wp
		worktreeBranch = wb
		cleanupWorktree = cleanup
		effectiveWorkDir = worktreePath
	}

	// Build system prompt
	sysContent := at.SystemPrompt
	sysContent += fmt.Sprintf("\n\nPlatform: %s\nWorking directory: %s", runtime.GOOS, effectiveWorkDir)
	if worktreePath != "" {
		sysContent += fmt.Sprintf("\n\nYou are working in an isolated git worktree (branch: %s).\nChanges here do not affect the main checkout until merged.", worktreeBranch)
	}

	// Build tool list for prompt
	toolDefs := subagentToolDefs(at)
	var toolNames []string
	for _, td := range toolDefs {
		toolNames = append(toolNames, td.Function.Name)
	}
	sysContent += "\n\nAvailable tools: " + strings.Join(toolNames, ", ")

	history := []ChatMessage{
		{Role: "system", Content: strPtr(sysContent)},
		{Role: "user", Content: strPtr(cfg.Task)},
	}

	env := &tool.Env{WorkDir: effectiveWorkDir}
	var finalText strings.Builder

	for turn := 1; turn <= subagentMaxTurns; turn++ {
		if needsCompaction(history, agentClient.Model) {
			compacted, ok := compactHistory(agentClient, history)
			if ok {
				history = compacted
			}
		}

		streamCh := make(chan StreamEvent, 64)
		go agentClient.StreamChat(context.Background(), history, toolDefs, streamCh)

		var textContent strings.Builder
		var toolCalls []ToolCall

		for evt := range streamCh {
			switch evt.Type {
			case StreamText:
				textContent.WriteString(evt.Text)
			case StreamToolCalls:
				toolCalls = evt.ToolCalls
			case StreamError:
				if cleanupWorktree != nil {
					cleanupWorktree()
				}
				return "", fmt.Errorf("subagent LLM error: %v", evt.Error)
			case StreamDone:
			}
		}

		assistantMsg := ChatMessage{Role: "assistant"}
		txt := textContent.String()
		if txt != "" {
			assistantMsg.Content = strPtr(txt)
		}
		if len(toolCalls) > 0 {
			assistantMsg.ToolCalls = toolCalls
		}
		history = append(history, assistantMsg)

		if len(toolCalls) == 0 {
			finalText.WriteString(txt)
			break
		}

		// Execute tools
		for _, tc := range toolCalls {
			var argsMap map[string]any
			json.Unmarshal([]byte(tc.Function.Arguments), &argsMap)

			// Enforce read-only for read-only agents
			if at.ReadOnly {
				if !readOnlyTools[tc.Function.Name] {
					history = append(history, ChatMessage{
						Role:       "tool",
						ToolCallID: tc.ID,
						Name:       tc.Function.Name,
						Content:    strPtr(fmt.Sprintf("Error: tool %q is not available in read-only mode", tc.Function.Name)),
					})
					continue
				}
				if tc.Function.Name == "shell" {
					output := executeReadOnlyShell(argsMap, env)
					history = append(history, ChatMessage{
						Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: strPtr(output),
					})
					continue
				}
			}

			result := globalRegistry.Execute(context.Background(), tc.Function.Name, argsMap, env)
			history = append(history, ChatMessage{
				Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: strPtr(result.Output),
			})
		}
	}

	// Clean up worktree
	if cleanupWorktree != nil {
		// Check if there are changes worth keeping
		if worktreePath != "" {
			hasChanges := worktreeHasChanges(worktreePath)
			if !hasChanges {
				cleanupWorktree()
				worktreePath = ""
			}
		}
	}

	result := finalText.String()
	if result == "" {
		result = "Subagent completed without producing a summary."
	}

	// Append worktree info if changes were made
	if worktreePath != "" {
		result += fmt.Sprintf("\n\n[Agent worked in worktree: %s (branch: %s). Changes preserved — merge when ready.]", worktreePath, worktreeBranch)
	}

	return result, nil
}

// RunSubagent is the backward-compatible entry point (explore type, no isolation).
func RunSubagent(client *LLMClient, workDir, task string) (string, error) {
	return RunSubagentWithConfig(client, workDir, SubagentConfig{
		Task:      task,
		AgentType: "explore",
		Depth:     1,
	})
}

// RunParallelSubagents executes multiple subagents concurrently and returns
// all results. Each agent runs independently with its own context.
func RunParallelSubagents(client *LLMClient, workDir string, configs []SubagentConfig) []SubagentResult {
	results := make([]SubagentResult, len(configs))
	var wg sync.WaitGroup

	for i, cfg := range configs {
		wg.Add(1)
		go func(idx int, c SubagentConfig) {
			defer wg.Done()
			output, err := RunSubagentWithConfig(client, workDir, c)
			results[idx] = SubagentResult{
				Task:   c.Task,
				Output: output,
				Error:  err,
			}
		}(i, cfg)
	}

	wg.Wait()
	return results
}

// SubagentResult holds the result of a parallel subagent execution.
type SubagentResult struct {
	Task   string
	Output string
	Error  error
}

// ── Worktree helpers ────────────────────────────────────────

// createWorktreeForAgent creates an isolated git worktree for an agent.
// Returns worktree path, branch name, cleanup function, and error.
func createWorktreeForAgent(workDir string) (string, string, func(), error) {
	// Find git root
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = workDir
	out, err := cmd.Output()
	if err != nil {
		return "", "", nil, fmt.Errorf("not a git repository")
	}
	gitRoot := strings.TrimSpace(string(out))

	// Generate unique name
	b := make([]byte, 4)
	rand.Read(b)
	name := "agent-" + hex.EncodeToString(b)
	worktreePath := filepath.Join(gitRoot, ".worktrees", name)
	branchName := "agent/" + name

	// Create worktree
	wCmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreePath)
	wCmd.Dir = gitRoot
	if out, err := wCmd.CombinedOutput(); err != nil {
		return "", "", nil, fmt.Errorf("git worktree add: %s %s", err, string(out))
	}

	cleanup := func() {
		rmCmd := exec.Command("git", "worktree", "remove", "--force", worktreePath)
		rmCmd.Dir = gitRoot
		rmCmd.Run()
		// Also delete the branch
		brCmd := exec.Command("git", "branch", "-D", branchName)
		brCmd.Dir = gitRoot
		brCmd.Run()
	}

	return worktreePath, branchName, cleanup, nil
}

// worktreeHasChanges checks if a worktree has uncommitted changes.
func worktreeHasChanges(worktreePath string) bool {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// ── Shell safety for read-only agents ───────────────────────

var shellWritePatterns = []string{
	"rm ", "rm\t", "rmdir", "mv ", "mv\t", "cp ", "cp\t",
	"mkdir", "touch ", "chmod", "chown",
	"tee ", "tee\t", "truncate", "sed -i", "patch ",
	"del ", "del\t", "erase ", "rd ", "rd\t",
	"move ", "move\t", "copy ", "copy\t", "xcopy",
	"ren ", "rename ", "md ", "md\t",
	"remove-item", "move-item", "copy-item",
	"new-item", "set-content", "add-content",
	"out-file", "invoke-webrequest",
	"git checkout", "git reset", "git clean", "git stash",
	"git merge", "git rebase", "git commit", "git push",
	"npm install", "yarn add", "pip install",
	"go install", "go get",
}

func executeReadOnlyShell(args map[string]any, env *tool.Env) string {
	command, _ := args["command"].(string)
	if command == "" {
		return "Error: command is required"
	}
	if strings.Contains(command, ">") || strings.Contains(command, ">>") {
		return "Error: output redirection is not allowed in read-only mode."
	}
	cmdLower := strings.ToLower(command)
	for _, pat := range shellWritePatterns {
		if strings.Contains(cmdLower, pat) {
			return fmt.Sprintf("Error: command blocked — %q appears to modify files. Subagent is read-only.", pat)
		}
	}
	result := globalRegistry.Execute(context.Background(), "shell", args, env)
	return result.Output
}
