package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

// NotebookEdit edits Jupyter notebooks (.ipynb) at the cell level.
// Handles the JSON structure properly — replace, insert, or delete cells
// without corrupting the notebook format.
type NotebookEdit struct{}

func (NotebookEdit) Name() string                          { return "notebook_edit" }
func (NotebookEdit) ConcurrencySafe(_ map[string]any) bool { return false }
func (NotebookEdit) Effects() []tool.Effect                { return []tool.Effect{tool.EffectWritesFS} }

func (NotebookEdit) Description() string {
	return "Edit Jupyter notebook (.ipynb) files at the cell level. Supports replacing cell content, " +
		"inserting new cells, and deleting cells. Use this instead of edit_file for notebooks — " +
		"it handles the JSON structure correctly and clears stale outputs."
}

func (NotebookEdit) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Path to .ipynb file relative to project root."
			},
			"cell_id": {
				"type": "string",
				"description": "Cell ID to edit. For insert mode, new cell is inserted after this cell. Omit to insert at beginning. Use 'cell-N' format for index-based access."
			},
			"new_source": {
				"type": "string",
				"description": "New cell source content."
			},
			"cell_type": {
				"type": "string",
				"description": "Cell type: 'code' or 'markdown'. Required for insert mode.",
				"enum": ["code", "markdown"]
			},
			"edit_mode": {
				"type": "string",
				"description": "Edit mode: 'replace' (default), 'insert', or 'delete'.",
				"enum": ["replace", "insert", "delete"]
			}
		},
		"required": ["path", "new_source"]
	}`)
}

// Notebook JSON structures — minimal, preserves unknown fields via json.RawMessage
type notebook struct {
	Cells         []notebookCell         `json:"cells"`
	Metadata      map[string]any         `json:"metadata,omitempty"`
	NBFormat      int                    `json:"nbformat"`
	NBFormatMinor int                    `json:"nbformat_minor"`
}

type notebookCell struct {
	CellType       string         `json:"cell_type"`
	Source          []string       `json:"source"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	ID              string         `json:"id,omitempty"`
	ExecutionCount  *int           `json:"execution_count,omitempty"`
	Outputs         []any          `json:"outputs,omitempty"`
}

func (NotebookEdit) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	relPath, _ := args["path"].(string)
	if relPath == "" {
		return tool.Result{Output: "Error: path is required", Success: false}
	}
	if !strings.HasSuffix(relPath, ".ipynb") {
		return tool.Result{Output: "Error: path must be a .ipynb file", Success: false}
	}

	absPath, err := SafePath(env.WorkDir, relPath)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	newSource, _ := args["new_source"].(string)
	cellID, _ := args["cell_id"].(string)
	cellType, _ := args["cell_type"].(string)
	editMode, _ := args["edit_mode"].(string)
	if editMode == "" {
		editMode = "replace"
	}

	// Read notebook
	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.Result{Output: fmt.Sprintf("Error: notebook not found: %s", relPath), Success: false}
		}
		return tool.Result{Output: fmt.Sprintf("Error reading: %v", err), Success: false}
	}

	var nb notebook
	if err := json.Unmarshal(data, &nb); err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: invalid notebook JSON: %v", err), Success: false}
	}

	// Find cell index
	cellIndex := -1
	if cellID != "" {
		// Try exact ID match
		for i, c := range nb.Cells {
			if c.ID == cellID {
				cellIndex = i
				break
			}
		}
		// Try cell-N format
		if cellIndex < 0 && strings.HasPrefix(cellID, "cell-") {
			if n, err := strconv.Atoi(cellID[5:]); err == nil && n >= 0 && n < len(nb.Cells) {
				cellIndex = n
			}
		}
		// Try plain number
		if cellIndex < 0 {
			if n, err := strconv.Atoi(cellID); err == nil && n >= 0 && n < len(nb.Cells) {
				cellIndex = n
			}
		}
	}

	// Split source into lines (notebook stores source as array of lines)
	sourceLines := splitNotebookSource(newSource)

	switch editMode {
	case "replace":
		if cellIndex < 0 {
			return tool.Result{Output: "Error: cell not found. Use cell_id to specify which cell to replace.", Success: false}
		}
		cell := &nb.Cells[cellIndex]
		cell.Source = sourceLines
		if cellType != "" {
			cell.CellType = cellType
		}
		// Clear stale outputs for code cells
		if cell.CellType == "code" {
			cell.ExecutionCount = nil
			cell.Outputs = []any{}
		}

	case "insert":
		if cellType == "" {
			cellType = "code"
		}
		newCell := notebookCell{
			CellType: cellType,
			Source:   sourceLines,
			ID:       randomCellID(),
			Metadata: map[string]any{},
		}
		if cellType == "code" {
			newCell.Outputs = []any{}
		}

		insertAt := 0
		if cellIndex >= 0 {
			insertAt = cellIndex + 1
		}
		// Insert into slice
		nb.Cells = append(nb.Cells, notebookCell{})
		copy(nb.Cells[insertAt+1:], nb.Cells[insertAt:])
		nb.Cells[insertAt] = newCell

	case "delete":
		if cellIndex < 0 {
			return tool.Result{Output: "Error: cell not found. Use cell_id to specify which cell to delete.", Success: false}
		}
		nb.Cells = append(nb.Cells[:cellIndex], nb.Cells[cellIndex+1:]...)

	default:
		return tool.Result{Output: fmt.Sprintf("Error: unknown edit_mode %q", editMode), Success: false}
	}

	// Write back
	outJSON, err := json.MarshalIndent(nb, "", " ")
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error marshaling: %v", err), Success: false}
	}
	// Notebook files conventionally end with a newline
	outJSON = append(outJSON, '\n')

	if err := os.WriteFile(absPath, outJSON, 0644); err != nil {
		return tool.Result{Output: fmt.Sprintf("Error writing: %v", err), Success: false}
	}

	var msg string
	switch editMode {
	case "replace":
		msg = fmt.Sprintf("Replaced cell %s in %s (%s, %d lines)", cellID, relPath, nb.Cells[cellIndex].CellType, len(sourceLines))
	case "insert":
		msg = fmt.Sprintf("Inserted %s cell in %s (%d lines)", cellType, relPath, len(sourceLines))
	case "delete":
		msg = fmt.Sprintf("Deleted cell %s from %s", cellID, relPath)
	}

	return tool.Result{Output: msg, Success: true, Files: []string{relPath}}
}

func splitNotebookSource(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := strings.Split(s, "\n")
	// Notebook format: each line except the last ends with \n
	result := make([]string, len(lines))
	for i, line := range lines {
		if i < len(lines)-1 {
			result[i] = line + "\n"
		} else {
			result[i] = line
		}
	}
	return result
}

func randomCellID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}
