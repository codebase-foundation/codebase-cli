package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

type WriteFile struct{}

func (WriteFile) Name() string                          { return "write_file" }
func (WriteFile) ConcurrencySafe(_ map[string]any) bool { return false }
func (WriteFile) Effects() []tool.Effect                { return []tool.Effect{tool.EffectWritesFS} }

func (WriteFile) Description() string {
	return "Create a new file or completely overwrite an existing file. " +
		"Use this for creating new files. For modifying existing files, prefer edit_file instead. " +
		"Parent directories are created automatically."
}

func (WriteFile) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "File path relative to project root."
			},
			"content": {
				"type": "string",
				"description": "The complete file content to write."
			}
		},
		"required": ["path", "content"]
	}`)
}

func (WriteFile) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	relPath, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if relPath == "" {
		return tool.Result{Output: "Error: path is required", Success: false}
	}
	absPath, err := SafePath(env.WorkDir, relPath)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	// Snapshot before modification (for undo)
	if env.History != nil {
		env.History.Snapshot(absPath, relPath, env.Turn)
	}

	info, existErr := os.Stat(absPath)
	existed := existErr == nil
	perm := os.FileMode(0644)
	if existed {
		perm = info.Mode().Perm()
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return tool.Result{Output: fmt.Sprintf("Error creating directory: %v", err), Success: false}
	}

	var oldLineCount int
	if existed {
		if oldData, readErr := os.ReadFile(absPath); readErr == nil {
			oldLineCount = strings.Count(string(oldData), "\n") + 1
		}
	}

	if err := os.WriteFile(absPath, []byte(content), perm); err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	lines := strings.Count(content, "\n") + 1
	var msg string
	if existed {
		lineDiff := lines - oldLineCount
		diffNote := ""
		if lineDiff > 0 {
			diffNote = fmt.Sprintf(", +%d", lineDiff)
		} else if lineDiff < 0 {
			diffNote = fmt.Sprintf(", %d", lineDiff)
		}
		msg = fmt.Sprintf("Updated %s (%d→%d lines, %d bytes%s)", relPath, oldLineCount, lines, len(content), diffNote)
	} else {
		msg = fmt.Sprintf("Created %s (%d lines, %d bytes)", relPath, lines, len(content))
	}

	return tool.Result{Output: msg, Success: true, Files: []string{relPath}}
}
