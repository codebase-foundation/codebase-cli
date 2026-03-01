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

const maxOutputChars = 30000

// ──────────────────────────────────────────────────────────────
//  Tool definitions (OpenAI function-calling schema)
// ──────────────────────────────────────────────────────────────

var toolDefs = []ToolDef{
	{
		Type: "function",
		Function: ToolDefFunction{
			Name:        "read_file",
			Description: "Read the contents of a file. Returns the file content with line numbers.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path to the file",
					},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name:        "write_file",
			Description: "Create or overwrite a file with the given content.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path to the file",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "The full content to write",
					},
				},
				"required": []string{"path", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name:        "edit_file",
			Description: "Edit a file by replacing an exact string match with new text.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Relative path to the file",
					},
					"old_text": map[string]interface{}{
						"type":        "string",
						"description": "The exact text to find and replace",
					},
					"new_text": map[string]interface{}{
						"type":        "string",
						"description": "The replacement text",
					},
				},
				"required": []string{"path", "old_text", "new_text"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name:        "shell",
			Description: "Run a shell command and return its output (stdout + stderr combined). Use for installing packages, running tests, git, etc.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "The shell command to run",
					},
				},
				"required": []string{"command"},
			},
		},
	},
}

// ──────────────────────────────────────────────────────────────
//  Tool execution
// ──────────────────────────────────────────────────────────────

// safePath resolves a relative path within workDir and ensures it
// doesn't escape via traversal.
func safePath(workDir, relPath string) (string, error) {
	resolved := filepath.Join(workDir, relPath)
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	absRoot, _ := filepath.Abs(workDir)
	if !strings.HasPrefix(abs, absRoot+string(filepath.Separator)) && abs != absRoot {
		return "", fmt.Errorf("path %q resolves outside project root", relPath)
	}
	return abs, nil
}

func truncateOutput(s string) string {
	if len(s) > maxOutputChars {
		return s[:maxOutputChars] + "\n\n--- OUTPUT TRUNCATED (30KB limit) ---"
	}
	return s
}

// ExecuteTool runs a single tool and returns (output, success).
func ExecuteTool(name string, argsJSON string, workDir string) (string, bool) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error: invalid arguments JSON: %v", err), false
	}

	switch name {
	case "read_file":
		return toolReadFile(args, workDir)
	case "write_file":
		return toolWriteFile(args, workDir)
	case "edit_file":
		return toolEditFile(args, workDir)
	case "shell":
		return toolShell(args, workDir)
	default:
		return fmt.Sprintf("Error: unknown tool %q", name), false
	}
}

func getString(args map[string]interface{}, key string) string {
	v, ok := args[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// ── read_file ────────────────────────────────────────────────

func toolReadFile(args map[string]interface{}, workDir string) (string, bool) {
	relPath := getString(args, "path")
	if relPath == "" {
		return "Error: path is required", false
	}
	absPath, err := safePath(workDir, relPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	lines := strings.Split(string(data), "\n")
	var sb strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&sb, "%4d │ %s\n", i+1, line)
	}
	return truncateOutput(sb.String()), true
}

// ── write_file ───────────────────────────────────────────────

func toolWriteFile(args map[string]interface{}, workDir string) (string, bool) {
	relPath := getString(args, "path")
	content := getString(args, "content")
	if relPath == "" {
		return "Error: path is required", false
	}
	absPath, err := safePath(workDir, relPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	// Ensure parent directory exists
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Sprintf("Error creating directory: %v", err), false
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	lines := strings.Count(content, "\n") + 1
	return fmt.Sprintf("Created %s (%d lines)", relPath, lines), true
}

// ── edit_file ────────────────────────────────────────────────

func toolEditFile(args map[string]interface{}, workDir string) (string, bool) {
	relPath := getString(args, "path")
	oldText := getString(args, "old_text")
	newText := getString(args, "new_text")
	if relPath == "" || oldText == "" {
		return "Error: path and old_text are required", false
	}
	absPath, err := safePath(workDir, relPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	content := string(data)
	if !strings.Contains(content, oldText) {
		return "Error: old_text not found in file", false
	}

	newContent := strings.Replace(content, oldText, newText, 1)
	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		return fmt.Sprintf("Error writing: %v", err), false
	}

	return fmt.Sprintf("Edited %s (replaced %d chars → %d chars)", relPath, len(oldText), len(newText)), true
}

// ── shell ────────────────────────────────────────────────────

func toolShell(args map[string]interface{}, workDir string) (string, bool) {
	command := getString(args, "command")
	if command == "" {
		return "Error: command is required", false
	}

	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = workDir

	// Combine stdout + stderr, timeout at 2 minutes
	done := make(chan struct{})
	var output []byte
	var cmdErr error

	go func() {
		output, cmdErr = cmd.CombinedOutput()
		close(done)
	}()

	select {
	case <-done:
		result := string(output)
		if cmdErr != nil {
			result += "\nExit code: " + cmdErr.Error()
			return truncateOutput(result), false
		}
		return truncateOutput(result), true
	case <-time.After(2 * time.Minute):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return "Error: command timed out after 2 minutes", false
	}
}
