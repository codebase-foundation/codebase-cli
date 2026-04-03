// Package tools contains built-in tool implementations.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

// ──────────────────────────────────────────────────────────────
//  read_file — read file contents with line numbers
// ──────────────────────────────────────────────────────────────

const maxOutputChars = 30000

// ReadFile reads file contents with line numbers, offset, and limit support.
type ReadFile struct{}

func (ReadFile) Name() string        { return "read_file" }
func (ReadFile) ConcurrencySafe(_ map[string]any) bool { return true }
func (ReadFile) Effects() []tool.Effect { return []tool.Effect{tool.EffectReadsFS} }

func (ReadFile) Description() string {
	return "Read the contents of a file. Returns the content with line numbers. " +
		"Always read a file before editing it to understand its current state. " +
		"For large files, use offset and limit to read specific sections."
}

func (ReadFile) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "File path relative to project root."
			},
			"offset": {
				"type": "number",
				"description": "Starting line number (1-based). Omit to start from the beginning."
			},
			"limit": {
				"type": "number",
				"description": "Maximum number of lines to read. Omit to read the entire file."
			}
		},
		"required": ["path"]
	}`)
}

func (ReadFile) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	relPath, _ := args["path"].(string)
	if relPath == "" {
		return tool.Result{Output: "Error: path is required", Success: false}
	}

	absPath, err := SafePath(env.WorkDir, relPath)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.Result{Output: fmt.Sprintf("Error: File not found: %s", relPath), Success: false}
		}
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}
	if info.IsDir() {
		return tool.Result{
			Output:  fmt.Sprintf("Error: %q is a directory. Use list_files to explore directories.", relPath),
			Success: false,
		}
	}
	if info.Size() > 5*1024*1024 {
		return tool.Result{
			Output: fmt.Sprintf("Error: %q is %.1fMB — too large to read entirely. Use offset/limit to read sections, or use shell with head/tail.",
				relPath, float64(info.Size())/1024/1024),
			Success: false,
		}
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	// Detect binary files by checking for null bytes in the first 512 bytes
	checkLen := len(data)
	if checkLen > 512 {
		checkLen = 512
	}
	for i := 0; i < checkLen; i++ {
		if data[i] == 0 {
			return tool.Result{
				Output:  fmt.Sprintf("Error: %q appears to be a binary file. Use shell with 'file', 'hexdump', or 'xxd' to inspect it.", relPath),
				Success: false,
			}
		}
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	// Apply offset/limit
	startLine := 0
	if offset, ok := args["offset"].(float64); ok && offset > 0 {
		startLine = int(offset) - 1 // 1-based to 0-based
		if startLine >= len(lines) {
			startLine = len(lines)
		}
	}
	endLine := len(lines)
	if limit, ok := args["limit"].(float64); ok && limit > 0 {
		endLine = startLine + int(limit)
		if endLine > len(lines) {
			endLine = len(lines)
		}
	}

	slice := lines[startLine:endLine]

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("File: %s (%d lines total)\n", relPath, totalLines))
	if startLine > 0 || endLine < totalLines {
		sb.WriteString(fmt.Sprintf("Showing lines %d-%d\n", startLine+1, min(endLine, totalLines)))
	}
	sb.WriteString("\n")
	for i, line := range slice {
		fmt.Fprintf(&sb, "%4d │ %s\n", startLine+i+1, line)
	}

	return tool.Result{
		Output:  TruncateOutput(sb.String(), maxOutputChars),
		Success: true,
		Files:   []string{relPath},
	}
}
