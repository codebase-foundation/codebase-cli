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

// MemoryDirFn is set at registration time to get the memory directory
// for the current project. Avoids circular import with main package.
var MemoryDirFn func(workDir string) (string, error)

// ── save_memory ─────────────────────────────────────────────

type SaveMemory struct{}

func (SaveMemory) Name() string                          { return "save_memory" }
func (SaveMemory) ConcurrencySafe(_ map[string]any) bool { return false }
func (SaveMemory) Effects() []tool.Effect                { return nil }

func (SaveMemory) Description() string {
	return "Save a memory for future sessions. Memories persist across conversations. " +
		"Use for: user preferences, project context, important decisions, feedback on your approach. " +
		"Each memory is a file with frontmatter (name, description, type). " +
		"Types: user (about the user), feedback (guidance on approach), project (ongoing work), reference (external pointers)."
}

func (SaveMemory) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"filename": {
				"type": "string",
				"description": "Filename for the memory (e.g. 'user_role.md', 'feedback_testing.md'). Use .md extension."
			},
			"content": {
				"type": "string",
				"description": "Memory content with YAML frontmatter. Format:\n---\nname: Memory Name\ndescription: One-line description\ntype: user|feedback|project|reference\n---\n\nContent here."
			}
		},
		"required": ["filename", "content"]
	}`)
}

func (SaveMemory) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	filename, _ := args["filename"].(string)
	content, _ := args["content"].(string)
	if filename == "" || content == "" {
		return tool.Result{Output: "Error: filename and content are required", Success: false}
	}

	if !strings.HasSuffix(filename, ".md") {
		filename += ".md"
	}

	if MemoryDirFn == nil {
		return tool.Result{Output: "Error: memory system not configured", Success: false}
	}

	dir, err := MemoryDirFn(env.WorkDir)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	clean := filepath.Clean(filename)
	if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
		return tool.Result{Output: "Error: invalid filename", Success: false}
	}

	path := filepath.Join(dir, clean)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return tool.Result{Output: fmt.Sprintf("Error writing memory: %v", err), Success: false}
	}

	return tool.Result{
		Output:  fmt.Sprintf("Saved memory: %s\n\nRemember to update MEMORY.md index if this is a new memory.", filename),
		Success: true,
	}
}

// ── read_memory ─────────────────────────────────────────────

type ReadMemory struct{}

func (ReadMemory) Name() string                          { return "read_memory" }
func (ReadMemory) ConcurrencySafe(_ map[string]any) bool { return true }
func (ReadMemory) Effects() []tool.Effect                { return nil }

func (ReadMemory) Description() string {
	return "Read a memory file from a previous session. The MEMORY.md index lists available memories. " +
		"Use this to recall details about user preferences, project context, or past decisions."
}

func (ReadMemory) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"filename": {
				"type": "string",
				"description": "Memory filename to read (from MEMORY.md index)."
			}
		},
		"required": ["filename"]
	}`)
}

func (ReadMemory) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	filename, _ := args["filename"].(string)
	if filename == "" {
		return tool.Result{Output: "Error: filename is required", Success: false}
	}

	if MemoryDirFn == nil {
		return tool.Result{Output: "Error: memory system not configured", Success: false}
	}

	dir, err := MemoryDirFn(env.WorkDir)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	clean := filepath.Clean(filename)
	if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
		return tool.Result{Output: "Error: invalid filename", Success: false}
	}

	data, err := os.ReadFile(filepath.Join(dir, clean))
	if err != nil {
		if os.IsNotExist(err) {
			return tool.Result{Output: fmt.Sprintf("Memory not found: %s", filename), Success: false}
		}
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	return tool.Result{Output: string(data), Success: true}
}
