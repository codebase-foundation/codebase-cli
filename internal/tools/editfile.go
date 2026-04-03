package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

type EditFile struct{}

func (EditFile) Name() string                          { return "edit_file" }
func (EditFile) ConcurrencySafe(_ map[string]any) bool { return false }
func (EditFile) Effects() []tool.Effect                { return []tool.Effect{tool.EffectWritesFS} }

func (EditFile) Description() string {
	return "Make a targeted edit to an existing file by finding and replacing specific text. " +
		"The old_text must match EXACTLY (including whitespace and indentation). " +
		"If old_text appears multiple times, the edit will fail — provide more surrounding context to make it unique. " +
		"Always read_file first to see the exact content before editing."
}

func (EditFile) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "File path relative to project root."
			},
			"old_text": {
				"type": "string",
				"description": "The exact text to find. Must be unique in the file."
			},
			"new_text": {
				"type": "string",
				"description": "The replacement text."
			}
		},
		"required": ["path", "old_text", "new_text"]
	}`)
}

func (EditFile) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	relPath, _ := args["path"].(string)
	oldText, _ := args["old_text"].(string)
	newText, _ := args["new_text"].(string)
	if relPath == "" || oldText == "" {
		return tool.Result{Output: "Error: path and old_text are required", Success: false}
	}
	absPath, err := SafePath(env.WorkDir, relPath)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	// Snapshot before modification (for undo)
	if env.History != nil {
		env.History.Snapshot(absPath, relPath, env.Turn)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.Result{Output: fmt.Sprintf("Error: File not found: %s", relPath), Success: false}
		}
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}
	perm := info.Mode().Perm()

	data, err := os.ReadFile(absPath)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	content := string(data)
	occurrences := strings.Count(content, oldText)

	if occurrences == 0 {
		lines := strings.Split(content, "\n")
		var preview string
		if len(lines) <= 30 {
			preview = content
		} else {
			preview = strings.Join(lines[:15], "\n") + "\n...\n" + strings.Join(lines[len(lines)-15:], "\n")
		}
		return tool.Result{
			Output: TruncateOutput(fmt.Sprintf(
				"Error: old_text not found in %s.\n\n"+
					"The text you're looking for doesn't match the file contents exactly. "+
					"Make sure whitespace and indentation match precisely.\n\n"+
					"File preview:\n%s", relPath, preview), maxOutputChars),
			Success: false,
		}
	}

	if occurrences > 1 {
		return tool.Result{
			Output:  fmt.Sprintf("Error: old_text found %d times in %s. Include more surrounding context to make the match unique.", occurrences, relPath),
			Success: false,
		}
	}

	newContent := strings.Replace(content, oldText, newText, 1)
	if err := os.WriteFile(absPath, []byte(newContent), perm); err != nil {
		return tool.Result{Output: fmt.Sprintf("Error writing: %v", err), Success: false}
	}

	oldLines := strings.Count(oldText, "\n") + 1
	newLines := strings.Count(newText, "\n") + 1
	lineDiff := newLines - oldLines
	diffNote := ""
	if lineDiff > 0 {
		diffNote = fmt.Sprintf(" (+%d lines)", lineDiff)
	} else if lineDiff < 0 {
		diffNote = fmt.Sprintf(" (%d lines)", lineDiff)
	}

	return tool.Result{
		Output:  fmt.Sprintf("Edited %s: replaced %d line(s) with %d line(s)%s", relPath, oldLines, newLines, diffNote),
		Success: true,
		Files:   []string{relPath},
	}
}
