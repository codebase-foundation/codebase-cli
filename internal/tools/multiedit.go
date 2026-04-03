package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

type MultiEdit struct{}

func (MultiEdit) Name() string                          { return "multi_edit" }
func (MultiEdit) ConcurrencySafe(_ map[string]any) bool { return false }
func (MultiEdit) Effects() []tool.Effect                { return []tool.Effect{tool.EffectWritesFS} }

func (MultiEdit) Description() string {
	return "Apply multiple edits across one or more files in a single operation. " +
		"Same semantics as edit_file per edit: exact string match, uniqueness enforced. " +
		"Use this instead of edit_file when you need to make 2 or more related changes. " +
		"Edits to the same file are applied sequentially. " +
		"Per-file atomicity: if any edit to a file fails, that file is rolled back. " +
		"Set replace_all to true on an edit to replace ALL occurrences. " +
		"Always read_file first to see the exact content before editing."
}

func (MultiEdit) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"edits": {
				"type": "array",
				"description": "Array of edits to apply.",
				"items": {
					"type": "object",
					"properties": {
						"path": {"type": "string", "description": "File path relative to project root."},
						"old_text": {"type": "string", "description": "The exact text to find."},
						"new_text": {"type": "string", "description": "The replacement text."},
						"replace_all": {"type": "boolean", "description": "If true, replace all occurrences instead of requiring uniqueness."}
					},
					"required": ["path", "old_text", "new_text"]
				}
			}
		},
		"required": ["edits"]
	}`)
}

type editEntry struct {
	Path       string `json:"path"`
	OldText    string `json:"old_text"`
	NewText    string `json:"new_text"`
	ReplaceAll bool   `json:"replace_all"`
}

func (MultiEdit) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	editsRaw, ok := args["edits"]
	if !ok {
		return tool.Result{Output: "Error: \"edits\" parameter is required.", Success: false}
	}

	editsJSON, err := json.Marshal(editsRaw)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: could not parse edits: %v", err), Success: false}
	}

	var edits []editEntry
	if err := json.Unmarshal(editsJSON, &edits); err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: could not parse edits array: %v", err), Success: false}
	}

	if len(edits) == 0 {
		return tool.Result{Output: "Error: \"edits\" must be a non-empty array.", Success: false}
	}

	for i, e := range edits {
		if e.Path == "" {
			return tool.Result{Output: fmt.Sprintf("Error: edit[%d] missing \"path\".", i), Success: false}
		}
		if e.OldText == "" {
			return tool.Result{Output: fmt.Sprintf("Error: edit[%d] missing \"old_text\".", i), Success: false}
		}
	}

	type indexedEdit struct {
		edit  editEntry
		index int
	}
	byFile := make(map[string][]indexedEdit)
	fileOrder := []string{}
	for i, e := range edits {
		if _, exists := byFile[e.Path]; !exists {
			fileOrder = append(fileOrder, e.Path)
		}
		byFile[e.Path] = append(byFile[e.Path], indexedEdit{edit: e, index: i})
	}

	type editResult struct {
		path   string
		status string
		detail string
	}
	results := make([]editResult, len(edits))
	var filesModified []string
	totalOk, totalFailed := 0, 0

	for _, filePath := range fileOrder {
		fileEdits := byFile[filePath]

		absPath, err := SafePath(env.WorkDir, filePath)
		if err != nil {
			for _, e := range fileEdits {
				results[e.index] = editResult{path: filePath, status: "error", detail: err.Error()}
				totalFailed++
			}
			continue
		}

		fileInfo, err := os.Stat(absPath)
		if err != nil {
			detail := fmt.Sprintf("Read error: %v", err)
			if os.IsNotExist(err) {
				detail = fmt.Sprintf("File not found: %s", filePath)
			}
			for _, e := range fileEdits {
				results[e.index] = editResult{path: filePath, status: "error", detail: detail}
				totalFailed++
			}
			continue
		}
		filePerm := fileInfo.Mode().Perm()

		data, err := os.ReadFile(absPath)
		if err != nil {
			for _, e := range fileEdits {
				results[e.index] = editResult{path: filePath, status: "error", detail: fmt.Sprintf("Read error: %v", err)}
				totalFailed++
			}
			continue
		}

		content := string(data)
		fileOk := true

		for _, e := range fileEdits {
			occurrences := strings.Count(content, e.edit.OldText)

			if occurrences == 0 {
				lines := strings.Split(content, "\n")
				var preview string
				if len(lines) <= 30 {
					preview = content
				} else {
					preview = strings.Join(lines[:15], "\n") + "\n...\n" + strings.Join(lines[len(lines)-15:], "\n")
				}
				results[e.index] = editResult{
					path:   filePath,
					status: "error",
					detail: fmt.Sprintf("old_text not found in %s. Whitespace/indentation must match exactly.\n\nFile preview:\n%s", filePath, preview),
				}
				fileOk = false
				totalFailed++
				break
			}

			if occurrences > 1 && !e.edit.ReplaceAll {
				results[e.index] = editResult{
					path:   filePath,
					status: "error",
					detail: fmt.Sprintf("old_text found %d times in %s. Set replace_all:true or include more context.", occurrences, filePath),
				}
				fileOk = false
				totalFailed++
				break
			}

			if e.edit.ReplaceAll {
				content = strings.ReplaceAll(content, e.edit.OldText, e.edit.NewText)
				oldLines := strings.Count(e.edit.OldText, "\n") + 1
				newLines := strings.Count(e.edit.NewText, "\n") + 1
				results[e.index] = editResult{
					path:   filePath,
					status: "ok",
					detail: fmt.Sprintf("Replaced %d occurrence(s): %d → %d line(s) each", occurrences, oldLines, newLines),
				}
			} else {
				content = strings.Replace(content, e.edit.OldText, e.edit.NewText, 1)
				oldLines := strings.Count(e.edit.OldText, "\n") + 1
				newLines := strings.Count(e.edit.NewText, "\n") + 1
				results[e.index] = editResult{
					path:   filePath,
					status: "ok",
					detail: fmt.Sprintf("Replaced %d line(s) with %d line(s)", oldLines, newLines),
				}
			}
			totalOk++
		}

		if !fileOk {
			for _, e := range fileEdits {
				if results[e.index].status == "" {
					results[e.index] = editResult{path: filePath, status: "skipped", detail: "Rolled back (earlier edit in this file failed)"}
				}
			}
			continue
		}

		if err := os.WriteFile(absPath, []byte(content), filePerm); err != nil {
			for _, e := range fileEdits {
				if results[e.index].status == "ok" {
					totalOk--
				}
				results[e.index] = editResult{path: filePath, status: "error", detail: fmt.Sprintf("Write failed: %v", err)}
				totalFailed++
			}
			continue
		}
		filesModified = append(filesModified, filePath)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## multi_edit results: %d ok, %d failed, %d total\n", totalOk, totalFailed, len(edits))
	if len(filesModified) > 0 {
		fmt.Fprintf(&sb, "Files modified: %s\n", strings.Join(filesModified, ", "))
	}
	sb.WriteString("\n")
	for i, r := range results {
		icon := "OK"
		if r.status == "error" {
			icon = "FAIL"
		} else if r.status == "skipped" {
			icon = "SKIP"
		}
		fmt.Fprintf(&sb, "[%s] edit[%d] %s: %s\n", icon, i, r.path, r.detail)
	}

	return tool.Result{
		Output:  TruncateOutput(sb.String(), maxOutputChars),
		Success: totalFailed == 0,
		Files:   filesModified,
	}
}
