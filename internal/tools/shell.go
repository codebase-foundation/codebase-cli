package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/codebase-foundation/cli/internal/tool"
)

type Shell struct{}

func (Shell) Name() string        { return "shell" }
func (Shell) Effects() []tool.Effect { return []tool.Effect{tool.EffectRunsProc} }

func (Shell) Description() string {
	if runtime.GOOS == "windows" {
		return "Execute a shell command in the project directory. " +
			"Use for: running builds, tests, installing packages, git commands, and any terminal task. " +
			"Commands run in PowerShell on Windows. The full stdout + stderr is returned. " +
			"Long-running commands are killed after the timeout."
	}
	return "Execute a shell command in the project directory. " +
		"Use for: running builds, tests, installing packages, git commands, and any terminal task. " +
		"Commands run in a bash shell. The full stdout + stderr is returned. " +
		"Long-running commands are killed after the timeout."
}

func (Shell) Schema() json.RawMessage {
	desc := "The shell command to execute. Use && to chain commands."
	if runtime.GOOS == "windows" {
		desc = "The shell command to execute. Use ; or && to chain commands. Use PowerShell syntax."
	}
	return tool.MustSchema(fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"command": {
				"type": "string",
				"description": %q
			}
		},
		"required": ["command"]
	}`, desc))
}

// ConcurrencySafe is INPUT-DEPENDENT: read-only commands can run in parallel.
// This is a key differentiator — CC uses 200K lines of AST parsing for this.
// We use a prefix check (fast) with optional glue model classification (smart).
func (Shell) ConcurrencySafe(args map[string]any) bool {
	cmd, _ := args["command"].(string)
	return !shellNeedsPermission(cmd)
}

// readOnlyPrefixes lists commands that are safe to run in parallel.
var readOnlyPrefixes = []string{
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

func shellNeedsPermission(cmd string) bool {
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))

	for _, prefix := range readOnlyPrefixes {
		if strings.HasPrefix(cmdLower, prefix) || cmdLower == strings.TrimSpace(prefix) {
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

	return true
}

var dangerousPatterns = func() []string {
	base := []string{
		"rm -rf /", "rm -rf ~", "rm -rf $HOME",
		":(){:|:&};:", "mkfs.", "dd if=", "> /dev/sd",
		"chmod -R 777 /", ":(){ :|:& };:",
	}
	if runtime.GOOS == "windows" {
		base = append(base,
			"format c:", "format d:",
			"del /f /s /q c:\\", "rd /s /q c:\\", "rmdir /s /q c:\\",
			"remove-item -recurse -force c:\\",
			"remove-item -recurse -force $env:",
		)
	}
	return base
}()

func (Shell) Execute(ctx context.Context, args map[string]any, env *tool.Env) tool.Result {
	command, _ := args["command"].(string)
	if command == "" {
		return tool.Result{Output: "Error: command is required", Success: false}
	}

	cmdLower := strings.ToLower(command)
	for _, pat := range dangerousPatterns {
		if strings.Contains(cmdLower, strings.ToLower(pat)) {
			return tool.Result{
				Output:  fmt.Sprintf("Error: blocked potentially destructive command matching %q. If this was intentional, run it manually.", pat),
				Success: false,
			}
		}
	}

	cmd := getShellCommand(command)
	cmd.Dir = env.WorkDir
	cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1", "FORCE_COLOR=0", "CI=1")
	setProcGroup(cmd)

	started := time.Now()
	done := make(chan struct{})
	var output []byte
	var cmdErr error

	go func() {
		output, cmdErr = cmd.CombinedOutput()
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(started).Seconds()
		result := string(output)
		if result == "" {
			result = "(no output)"
		}
		if cmdErr != nil {
			return tool.Result{
				Output:  TruncateOutput(fmt.Sprintf("%s\n\nExit code: %s | Wall time: %.1fs", result, cmdErr.Error(), elapsed), maxOutputChars),
				Success: false,
			}
		}
		return tool.Result{
			Output:  TruncateOutput(fmt.Sprintf("%s\n\nExit code: 0 | Wall time: %.1fs", result, elapsed), maxOutputChars),
			Success: true,
		}
	case <-time.After(2 * time.Minute):
		killProcGroup(cmd)
		return tool.Result{Output: "Error: command timed out after 2 minutes", Success: false}
	case <-ctx.Done():
		killProcGroup(cmd)
		return tool.Result{Output: "Error: command cancelled", Success: false}
	}
}

func getShellCommand(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("pwsh"); err == nil {
			return exec.Command("pwsh", "-NoProfile", "-NonInteractive", "-Command", command)
		}
		if _, err := exec.LookPath("powershell"); err == nil {
			return exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", command)
		}
		shell := os.Getenv("COMSPEC")
		if shell == "" {
			shell = "cmd.exe"
		}
		return exec.Command(shell, "/C", command)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return exec.Command(shell, "-c", command)
}
