package main

import (
	"fmt"
	"strings"
)

// ──────────────────────────────────────────────────────────────
//  Permission system — approve/deny tool execution
// ──────────────────────────────────────────────────────────────

type PermissionLevel int

const (
	PermAsk      PermissionLevel = iota // ask every time (default)
	PermTrustTool                       // auto-approve this specific tool
	PermTrustAll                        // auto-approve everything
)

// PermissionRequest is sent from the agent goroutine to the TUI.
type PermissionRequest struct {
	Tool        string
	Args        map[string]any
	Summary     string // human-readable description
	Risk        string // "LOW", "MEDIUM", "HIGH" — from glue explainer
	Explanation string // glue-generated explanation of what this does
}

// PermissionResponse is sent back from the TUI to the agent.
type PermissionResponse struct {
	Allowed    bool
	TrustLevel PermissionLevel
}

// PermissionState tracks per-session trust decisions.
type PermissionState struct {
	Level        PermissionLevel
	TrustedTools map[string]bool
}

// NeedsPermission returns true if a tool invocation requires user approval.
func NeedsPermission(toolName string, args map[string]any) bool {
	switch toolName {
	case "write_file", "edit_file", "multi_edit":
		return true
	case "git_commit", "git_branch":
		// git_branch only needs permission when switching/creating
		if toolName == "git_branch" {
			name, _ := args["name"].(string)
			return name != "" // listing branches = read-only
		}
		return true
	case "shell":
		cmd, _ := args["command"].(string)
		return shellNeedsPermission(cmd)
	default:
		return false
	}
}

// shellNeedsPermission returns false for known read-only shell commands.
func shellNeedsPermission(cmd string) bool {
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))

	readOnlyPrefixes := []string{
		// Unix
		"ls", "cat ", "head ", "tail ", "grep ", "rg ", "find ",
		"wc ", "file ", "which ", "echo ", "pwd", "env", "printenv",
		"du ", "df ", "stat ", "date", "uname",
		"jq ", "sort ", "uniq ", "tr ",
		// Windows / PowerShell
		"dir ", "dir", "type ", "where ", "where.exe",
		"get-content ", "get-childitem ", "get-item ",
		"get-location", "get-process",
		"select-string ", "measure-object",
		"test-path ", "resolve-path",
		"systeminfo", "hostname", "whoami",
		"write-host ", "write-output ",
		// Git (cross-platform)
		"git status", "git log", "git diff", "git show", "git branch",
		"git remote", "git tag", "git stash list",
		// Build/test (cross-platform)
		"go vet", "go build", "go test", "go run",
		"tsc ", "npx tsc", "npx ", "npm test", "npm run", "npm ls",
		"pytest", "python -c", "python3 -c", "python -m pytest",
		"cargo check", "cargo test", "cargo build",
		"make ", "make -n", "make check",
		"node -e", "node --eval",
		"dotnet build", "dotnet test",
	}

	for _, prefix := range readOnlyPrefixes {
		if strings.HasPrefix(cmdLower, prefix) {
			return false
		}
		// Also allow if the command IS exactly the prefix (no trailing space needed)
		if cmdLower == strings.TrimSpace(prefix) {
			return false
		}
	}

	// Piped commands that start read-only are still read-only
	parts := strings.SplitN(cmdLower, "|", 2)
	if len(parts) > 1 {
		first := strings.TrimSpace(parts[0])
		for _, prefix := range readOnlyPrefixes {
			if strings.HasPrefix(first, prefix) || first == strings.TrimSpace(prefix) {
				return false
			}
		}
	}

	return true // default: ask permission
}

// PermissionSummary generates a human-readable description of what a tool will do.
func PermissionSummary(toolName string, args map[string]any) string {
	switch toolName {
	case "write_file":
		path, _ := args["path"].(string)
		return fmt.Sprintf("Create/overwrite: %s", path)
	case "edit_file":
		path, _ := args["path"].(string)
		return fmt.Sprintf("Edit: %s", path)
	case "multi_edit":
		// Count unique files
		files := "multiple files"
		if edits, ok := args["edits"]; ok {
			if arr, ok := edits.([]interface{}); ok {
				seen := map[string]bool{}
				for _, e := range arr {
					if m, ok := e.(map[string]interface{}); ok {
						if p, ok := m["path"].(string); ok {
							seen[p] = true
						}
					}
				}
				if len(seen) > 0 {
					names := make([]string, 0, len(seen))
					for k := range seen {
						names = append(names, k)
					}
					files = strings.Join(names, ", ")
				}
			}
		}
		return fmt.Sprintf("Multi-edit: %s", files)
	case "git_commit":
		msg, _ := args["message"].(string)
		if len(msg) > 60 {
			msg = msg[:57] + "..."
		}
		return fmt.Sprintf("Git commit: %q", msg)
	case "git_branch":
		name, _ := args["name"].(string)
		if getBool(args, "create") {
			return fmt.Sprintf("Create branch: %s", name)
		}
		return fmt.Sprintf("Switch to branch: %s", name)
	case "shell":
		cmd, _ := args["command"].(string)
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		return fmt.Sprintf("Run: %s", cmd)
	default:
		return toolName
	}
}

// parsePermissionInput interprets user response to a permission prompt.
func parsePermissionInput(input string) PermissionResponse {
	input = strings.ToLower(strings.TrimSpace(input))
	switch input {
	case "", "y", "yes":
		return PermissionResponse{Allowed: true, TrustLevel: PermAsk}
	case "n", "no":
		return PermissionResponse{Allowed: false, TrustLevel: PermAsk}
	case "a", "always":
		return PermissionResponse{Allowed: true, TrustLevel: PermTrustTool}
	case "all", "trust":
		return PermissionResponse{Allowed: true, TrustLevel: PermTrustAll}
	default:
		// Default to allow — user just pressed enter or typed something
		return PermissionResponse{Allowed: true, TrustLevel: PermAsk}
	}
}
