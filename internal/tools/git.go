package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

func isGitRepo(workDir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = workDir
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func gitErr(args map[string]any, workDir string) *tool.Result {
	if !isGitRepo(workDir) {
		return &tool.Result{Output: "Error: not a git repository", Success: false}
	}
	return nil
}

// ── git_status ──────────────────────────────────────────────────

type GitStatus struct{}

func (GitStatus) Name() string                          { return "git_status" }
func (GitStatus) ConcurrencySafe(_ map[string]any) bool { return true }
func (GitStatus) Effects() []tool.Effect                { return []tool.Effect{tool.EffectGitRead} }
func (GitStatus) Description() string {
	return "Show the working tree status: staged, unstaged, and untracked files. Use this before making commits to see what has changed."
}
func (GitStatus) Schema() json.RawMessage {
	return tool.MustSchema(`{"type":"object","properties":{},"required":[]}`)
}
func (GitStatus) Execute(_ context.Context, _ map[string]any, env *tool.Env) tool.Result {
	if e := gitErr(nil, env.WorkDir); e != nil {
		return *e
	}
	cmd := exec.Command("git", "status", "--short", "--branch")
	cmd.Dir = env.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %s\n%s", err, string(out)), Success: false}
	}
	result := strings.TrimSpace(string(out))
	if result == "" {
		return tool.Result{Output: "No changes (clean working tree)", Success: true}
	}
	return tool.Result{Output: result, Success: true}
}

// ── git_diff ────────────────────────────────────────────────────

type GitDiff struct{}

func (GitDiff) Name() string                          { return "git_diff" }
func (GitDiff) ConcurrencySafe(_ map[string]any) bool { return true }
func (GitDiff) Effects() []tool.Effect                { return []tool.Effect{tool.EffectGitRead} }
func (GitDiff) Description() string {
	return "Show file differences. By default shows unstaged changes. Use staged=true for staged changes, or specify a ref to diff against."
}
func (GitDiff) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type":"object",
		"properties":{
			"staged":{"type":"boolean","description":"If true, show staged (--cached) changes."},
			"ref":{"type":"string","description":"Git ref to diff against (e.g. HEAD~3, main, a commit hash)."},
			"path":{"type":"string","description":"Limit diff to a specific file or directory."}
		}
	}`)
}
func (GitDiff) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	if e := gitErr(args, env.WorkDir); e != nil {
		return *e
	}
	gitArgs := []string{"diff", "--no-color"}
	if b, _ := args["staged"].(bool); b {
		gitArgs = append(gitArgs, "--cached")
	}
	if ref, _ := args["ref"].(string); ref != "" {
		gitArgs = append(gitArgs, ref)
	}
	if p, _ := args["path"].(string); p != "" {
		gitArgs = append(gitArgs, "--", p)
	}
	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = env.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %s\n%s", err, string(out)), Success: false}
	}
	result := strings.TrimSpace(string(out))
	if result == "" {
		return tool.Result{Output: "No differences found", Success: true}
	}
	return tool.Result{Output: TruncateOutput(result, maxOutputChars), Success: true}
}

// ── git_log ─────────────────────────────────────────────────────

type GitLog struct{}

func (GitLog) Name() string                          { return "git_log" }
func (GitLog) ConcurrencySafe(_ map[string]any) bool { return true }
func (GitLog) Effects() []tool.Effect                { return []tool.Effect{tool.EffectGitRead} }
func (GitLog) Description() string {
	return "Show recent commit history. Returns commit hash, author, date, and message."
}
func (GitLog) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type":"object",
		"properties":{
			"count":{"type":"number","description":"Number of commits to show (default 10, max 50)."},
			"oneline":{"type":"boolean","description":"If true, show compact one-line format."},
			"path":{"type":"string","description":"Show commits that modified this file/directory."}
		}
	}`)
}
func (GitLog) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	if e := gitErr(args, env.WorkDir); e != nil {
		return *e
	}
	count := 10
	if c, ok := args["count"].(float64); ok && c > 0 {
		count = int(c)
		if count > 50 {
			count = 50
		}
	}
	format := "--format=%H %an (%ar)%n  %s"
	if b, _ := args["oneline"].(bool); b {
		format = "--format=%h %s (%ar)"
	}
	gitArgs := []string{"log", format, fmt.Sprintf("-n%d", count), "--no-color"}
	if p, _ := args["path"].(string); p != "" {
		gitArgs = append(gitArgs, "--", p)
	}
	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = env.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %s\n%s", err, string(out)), Success: false}
	}
	return tool.Result{Output: TruncateOutput(strings.TrimSpace(string(out)), maxOutputChars), Success: true}
}

// ── git_commit ──────────────────────────────────────────────────

type GitCommit struct{}

func (GitCommit) Name() string                          { return "git_commit" }
func (GitCommit) ConcurrencySafe(_ map[string]any) bool { return false }
func (GitCommit) Effects() []tool.Effect                { return []tool.Effect{tool.EffectGitWrite} }
func (GitCommit) Description() string {
	return "Stage files and create a git commit. Specify files to stage, or use stage_all=true to stage all changes. The commit message is required."
}
func (GitCommit) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type":"object",
		"properties":{
			"message":{"type":"string","description":"Commit message."},
			"files":{"type":"array","description":"Files to stage before committing.","items":{"type":"string"}},
			"stage_all":{"type":"boolean","description":"If true, stage all changes (git add -A) before committing."}
		},
		"required":["message"]
	}`)
}
func (GitCommit) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	if e := gitErr(args, env.WorkDir); e != nil {
		return *e
	}
	message, _ := args["message"].(string)
	if message == "" {
		return tool.Result{Output: "Error: commit message is required", Success: false}
	}
	if b, _ := args["stage_all"].(bool); b {
		cmd := exec.Command("git", "add", "-A")
		cmd.Dir = env.WorkDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return tool.Result{Output: fmt.Sprintf("Error staging: %s\n%s", err, string(out)), Success: false}
		}
	} else if filesRaw, ok := args["files"]; ok {
		filesJSON, _ := json.Marshal(filesRaw)
		var files []string
		json.Unmarshal(filesJSON, &files)
		for _, f := range files {
			cmd := exec.Command("git", "add", "--", f)
			cmd.Dir = env.WorkDir
			if out, err := cmd.CombinedOutput(); err != nil {
				return tool.Result{Output: fmt.Sprintf("Error staging %s: %s\n%s", f, err, string(out)), Success: false}
			}
		}
	}
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = env.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %s\n%s", err, string(out)), Success: false}
	}
	return tool.Result{Output: strings.TrimSpace(string(out)), Success: true}
}

// ── git_branch ──────────────────────────────────────────────────

type GitBranch struct{}

func (GitBranch) Name() string { return "git_branch" }

// ConcurrencySafe: listing is safe, creating/switching is not.
func (GitBranch) ConcurrencySafe(args map[string]any) bool {
	name, _ := args["name"].(string)
	return name == "" // listing only
}

func (GitBranch) Effects() []tool.Effect {
	return []tool.Effect{tool.EffectGitRead, tool.EffectGitWrite}
}
func (GitBranch) Description() string {
	return "List, create, or switch branches. Without arguments, lists all local branches. With name, creates or switches to that branch."
}
func (GitBranch) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type":"object",
		"properties":{
			"name":{"type":"string","description":"Branch name to create or switch to."},
			"create":{"type":"boolean","description":"If true, create a new branch (git checkout -b)."}
		}
	}`)
}
func (GitBranch) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	if e := gitErr(args, env.WorkDir); e != nil {
		return *e
	}
	name, _ := args["name"].(string)
	if name == "" {
		cmd := exec.Command("git", "branch", "-v", "--no-color")
		cmd.Dir = env.WorkDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return tool.Result{Output: fmt.Sprintf("Error: %s\n%s", err, string(out)), Success: false}
		}
		return tool.Result{Output: strings.TrimSpace(string(out)), Success: true}
	}
	if b, _ := args["create"].(bool); b {
		cmd := exec.Command("git", "checkout", "-b", name)
		cmd.Dir = env.WorkDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			return tool.Result{Output: fmt.Sprintf("Error: %s\n%s", err, string(out)), Success: false}
		}
		return tool.Result{Output: fmt.Sprintf("Created and switched to branch: %s\n%s", name, strings.TrimSpace(string(out))), Success: true}
	}
	cmd := exec.Command("git", "switch", name)
	cmd.Dir = env.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %s\n%s", err, string(out)), Success: false}
	}
	return tool.Result{Output: fmt.Sprintf("Switched to branch: %s\n%s", name, strings.TrimSpace(string(out))), Success: true}
}
