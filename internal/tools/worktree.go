package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

// ── enter_worktree ──────────────────────────────────────────

// EnterWorktree creates an isolated git worktree for parallel branch work.
// The session's working directory switches to the worktree. Critical for
// subagent isolation — parallel agents can work without conflicting.
type EnterWorktree struct{}

func (EnterWorktree) Name() string                          { return "enter_worktree" }
func (EnterWorktree) ConcurrencySafe(_ map[string]any) bool { return false }
func (EnterWorktree) Effects() []tool.Effect                { return []tool.Effect{tool.EffectGitWrite} }

func (EnterWorktree) Description() string {
	return "Create an isolated git worktree and switch to it. " +
		"Use for working on separate branches in parallel without affecting the main checkout. " +
		"The worktree is a full copy of the repo at the current HEAD."
}

func (EnterWorktree) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "Worktree name (used as branch suffix). Auto-generated if omitted. Letters, digits, dots, underscores, dashes only."
			}
		}
	}`)
}

var worktreeNameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

func (EnterWorktree) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	if !isGitRepo(env.WorkDir) {
		return tool.Result{Output: "Error: not a git repository", Success: false}
	}

	// Check we're not already in a worktree
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = env.WorkDir
	// Check if we're the main worktree
	mainCmd := exec.Command("git", "rev-parse", "--git-common-dir")
	mainCmd.Dir = env.WorkDir
	commonDir, _ := mainCmd.Output()
	gitDir := exec.Command("git", "rev-parse", "--git-dir")
	gitDir.Dir = env.WorkDir
	actualGitDir, _ := gitDir.Output()
	if strings.TrimSpace(string(commonDir)) != strings.TrimSpace(string(actualGitDir)) {
		return tool.Result{Output: "Error: already in a worktree. Exit first with exit_worktree.", Success: false}
	}

	name, _ := args["name"].(string)
	if name == "" {
		b := make([]byte, 4)
		rand.Read(b)
		name = "wt-" + hex.EncodeToString(b)
	}
	if !worktreeNameRe.MatchString(name) {
		return tool.Result{Output: "Error: name must contain only letters, digits, dots, underscores, dashes (max 64 chars)", Success: false}
	}

	// Find git root
	rootCmd := exec.Command("git", "rev-parse", "--show-toplevel")
	rootCmd.Dir = env.WorkDir
	rootOut, err := rootCmd.Output()
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error finding git root: %v", err), Success: false}
	}
	gitRoot := strings.TrimSpace(string(rootOut))

	// Create worktree
	worktreePath := filepath.Join(gitRoot, ".worktrees", name)
	branchName := "worktree/" + name

	wCmd := exec.Command("git", "worktree", "add", "-b", branchName, worktreePath)
	wCmd.Dir = gitRoot
	out, err := wCmd.CombinedOutput()
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error creating worktree: %s\n%s", err, string(out)), Success: false}
	}

	return tool.Result{
		Output: fmt.Sprintf("Created worktree at %s (branch: %s)\n\nSwitch your working directory to this path to use it.", worktreePath, branchName),
		Success: true,
	}
}

// ── exit_worktree ───────────────────────────────────────────

// ExitWorktree exits and optionally removes a git worktree.
type ExitWorktree struct{}

func (ExitWorktree) Name() string                          { return "exit_worktree" }
func (ExitWorktree) ConcurrencySafe(_ map[string]any) bool { return false }
func (ExitWorktree) Effects() []tool.Effect                { return []tool.Effect{tool.EffectGitWrite} }

func (ExitWorktree) Description() string {
	return "Exit a git worktree created by enter_worktree. " +
		"Use action 'keep' to preserve the worktree branch, or 'remove' to clean it up. " +
		"If there are uncommitted changes, removal requires discard_changes=true."
}

func (ExitWorktree) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "What to do: 'keep' preserves the worktree, 'remove' deletes it.",
				"enum": ["keep", "remove"]
			},
			"discard_changes": {
				"type": "boolean",
				"description": "If true, force-remove even with uncommitted changes."
			}
		},
		"required": ["action"]
	}`)
}

func (ExitWorktree) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	if !isGitRepo(env.WorkDir) {
		return tool.Result{Output: "Error: not a git repository", Success: false}
	}

	action, _ := args["action"].(string)
	discardChanges, _ := args["discard_changes"].(bool)

	if action == "keep" {
		return tool.Result{Output: "Worktree kept. Switch back to your main checkout to continue.", Success: true}
	}

	if action == "remove" {
		// Check for uncommitted changes
		statusCmd := exec.Command("git", "status", "--porcelain")
		statusCmd.Dir = env.WorkDir
		statusOut, _ := statusCmd.Output()
		if len(strings.TrimSpace(string(statusOut))) > 0 && !discardChanges {
			lines := strings.Split(strings.TrimSpace(string(statusOut)), "\n")
			return tool.Result{
				Output: fmt.Sprintf("Error: worktree has %d uncommitted changes. Set discard_changes=true to force removal, or commit your work first.\n\n%s",
					len(lines), string(statusOut)),
				Success: false,
			}
		}

		// Check for unpushed commits
		logCmd := exec.Command("git", "log", "@{u}..HEAD", "--oneline")
		logCmd.Dir = env.WorkDir
		logOut, _ := logCmd.Output()
		unpushed := strings.TrimSpace(string(logOut))

		// Get worktree path for removal
		worktreePath, _ := filepath.Abs(env.WorkDir)

		// Find git root to run worktree remove from there
		rootCmd := exec.Command("git", "worktree", "list", "--porcelain")
		rootCmd.Dir = env.WorkDir
		rootCmd.Output() // just to verify we can access git

		// Remove worktree
		removeArgs := []string{"worktree", "remove"}
		if discardChanges {
			removeArgs = append(removeArgs, "--force")
		}
		removeArgs = append(removeArgs, worktreePath)
		removeCmd := exec.Command("git", removeArgs...)
		removeCmd.Dir = os.TempDir() // run from outside the worktree
		out, err := removeCmd.CombinedOutput()
		if err != nil {
			return tool.Result{Output: fmt.Sprintf("Error removing worktree: %s\n%s", err, string(out)), Success: false}
		}

		msg := "Worktree removed."
		if unpushed != "" {
			msg += fmt.Sprintf("\n\nNote: there were unpushed commits that are now only in the reflog:\n%s", unpushed)
		}
		return tool.Result{Output: msg, Success: true}
	}

	return tool.Result{Output: "Error: action must be 'keep' or 'remove'", Success: false}
}
