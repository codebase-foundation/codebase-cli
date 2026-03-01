package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxOutputChars = 30000
const maxResultLines = 500

// ──────────────────────────────────────────────────────────────
//  Tool definitions (OpenAI function-calling schema)
// ──────────────────────────────────────────────────────────────

var toolDefs = []ToolDef{
	{
		Type: "function",
		Function: ToolDefFunction{
			Name: "read_file",
			Description: "Read the contents of a file. Returns the content with line numbers. " +
				"Always read a file before editing it to understand its current state. " +
				"For large files, use offset and limit to read specific sections.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "File path relative to project root.",
					},
					"offset": map[string]interface{}{
						"type":        "number",
						"description": "Starting line number (1-based). Omit to start from the beginning.",
					},
					"limit": map[string]interface{}{
						"type":        "number",
						"description": "Maximum number of lines to read. Omit to read the entire file.",
					},
				},
				"required": []string{"path"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name: "write_file",
			Description: "Create a new file or completely overwrite an existing file. " +
				"Use this for creating new files. For modifying existing files, prefer edit_file instead. " +
				"Parent directories are created automatically.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "File path relative to project root.",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "The complete file content to write.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name: "edit_file",
			Description: "Make a targeted edit to an existing file by finding and replacing specific text. " +
				"The old_text must match EXACTLY (including whitespace and indentation). " +
				"If old_text appears multiple times, the edit will fail — provide more surrounding context to make it unique. " +
				"Always read_file first to see the exact content before editing.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "File path relative to project root.",
					},
					"old_text": map[string]interface{}{
						"type":        "string",
						"description": "The exact text to find. Must be unique in the file.",
					},
					"new_text": map[string]interface{}{
						"type":        "string",
						"description": "The replacement text.",
					},
				},
				"required": []string{"path", "old_text", "new_text"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name: "multi_edit",
			Description: "Apply multiple edits across one or more files in a single operation. " +
				"Same semantics as edit_file per edit: exact string match, uniqueness enforced. " +
				"Use this instead of edit_file when you need to make 2 or more related changes. " +
				"Edits to the same file are applied sequentially. " +
				"Per-file atomicity: if any edit to a file fails, that file is rolled back. " +
				"Set replace_all to true on an edit to replace ALL occurrences. " +
				"Always read_file first to see the exact content before editing.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"edits": map[string]interface{}{
						"type":        "array",
						"description": "Array of edits to apply.",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"path": map[string]interface{}{
									"type":        "string",
									"description": "File path relative to project root.",
								},
								"old_text": map[string]interface{}{
									"type":        "string",
									"description": "The exact text to find.",
								},
								"new_text": map[string]interface{}{
									"type":        "string",
									"description": "The replacement text.",
								},
								"replace_all": map[string]interface{}{
									"type":        "boolean",
									"description": "If true, replace ALL occurrences. Default: false.",
								},
							},
							"required": []string{"path", "old_text", "new_text"},
						},
					},
				},
				"required": []string{"edits"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name: "list_files",
			Description: "List files and directories. Without a pattern, lists the immediate contents of the directory. " +
				"With a glob pattern, recursively matches files (e.g. \"**/*.tsx\" finds all TSX files). " +
				"Use this to explore project structure before reading or editing files.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Directory path relative to project root. Defaults to \".\" (project root).",
					},
					"pattern": map[string]interface{}{
						"type":        "string",
						"description": "Glob pattern to filter results (e.g. \"**/*.js\", \"src/**/*.tsx\", \"*.json\").",
					},
				},
				"required": []string{},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name: "search_files",
			Description: "Search file contents using a regex pattern (powered by ripgrep with grep fallback). " +
				"Returns matching lines with file paths and line numbers. " +
				"Use to find function definitions, imports, usages, error messages, etc.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"pattern": map[string]interface{}{
						"type":        "string",
						"description": "Regex pattern to search for (e.g. \"func handleSubmit\", \"import.*React\", \"TODO\").",
					},
					"path": map[string]interface{}{
						"type":        "string",
						"description": "Directory to search in, relative to project root. Defaults to \".\" (entire project).",
					},
					"include": map[string]interface{}{
						"type":        "string",
						"description": "Glob to filter which files to search (e.g. \"*.ts\", \"*.{js,jsx}\").",
					},
				},
				"required": []string{"pattern"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name: "dispatch_agent",
			Description: "Spawn a focused research subagent to investigate a specific question or gather information. " +
				"The subagent runs in its own isolated context with read-only tools (read_file, list_files, search_files, shell). " +
				"It cannot modify files. Use this to explore large codebases, find patterns across many files, " +
				"or answer complex questions without polluting your main context window. " +
				"The subagent returns a text summary of its findings.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task": map[string]interface{}{
						"type":        "string",
						"description": "A clear description of what to research or investigate. Be specific about what information you need.",
					},
				},
				"required": []string{"task"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name: "web_search",
			Description: "Search the web for current information. " +
				"Use when you need up-to-date information, documentation, API references, error solutions, " +
				"or anything not available in the local project files. " +
				"Returns titles, URLs, and text snippets from search results.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "The search query.",
					},
					"max_results": map[string]interface{}{
						"type":        "number",
						"description": "Maximum number of results to return (1-10, default 5).",
					},
				},
				"required": []string{"query"},
			},
		},
	},
	{
		Type: "function",
		Function: ToolDefFunction{
			Name: "shell",
			Description: "Execute a shell command in the project directory. " +
				"Use for: running builds, tests, installing packages, git commands, and any terminal task. " +
				"Commands run in a bash shell. The full stdout + stderr is returned. " +
				"Long-running commands are killed after the timeout.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "The shell command to execute. Use && to chain commands.",
					},
				},
				"required": []string{"command"},
			},
		},
	},
}

// parallelSafeTools lists tools that are safe to run concurrently (read-only, no side effects).
var parallelSafeTools = map[string]bool{
	"read_file":      true,
	"list_files":     true,
	"search_files":   true,
	"web_search":     true,
	"dispatch_agent": true,
}

// IsParallelSafe returns whether a tool can run concurrently with other tools.
func IsParallelSafe(name string) bool {
	return parallelSafeTools[name]
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
		cutChars := len(s) - maxOutputChars
		return s[:maxOutputChars] + fmt.Sprintf("\n\n--- OUTPUT TRUNCATED (%d chars cut, %d total) ---\nUse read_file with offset/limit or refine your command to see specific sections.", cutChars, len(s))
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
	case "multi_edit":
		return toolMultiEdit(args, workDir)
	case "list_files":
		return toolListFiles(args, workDir)
	case "search_files":
		return toolSearchFiles(args, workDir)
	case "web_search":
		return toolWebSearch(args)
	case "dispatch_agent":
		// dispatch_agent is handled directly in the agent loop (needs LLM client)
		return "Error: dispatch_agent must be called through the agent loop", false
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

func getFloat(args map[string]interface{}, key string) (float64, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

func getBool(args map[string]interface{}, key string) bool {
	v, ok := args[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return b && ok
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

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("Error: File not found: %s", relPath), false
		}
		return fmt.Sprintf("Error: %v", err), false
	}
	if info.IsDir() {
		return fmt.Sprintf("Error: %q is a directory. Use list_files to explore directories.", relPath), false
	}
	if info.Size() > 5*1024*1024 {
		return fmt.Sprintf("Error: %q is %.1fMB — too large to read entirely. Use offset/limit to read sections, or use shell with head/tail.", relPath, float64(info.Size())/1024/1024), false
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	lines := strings.Split(string(data), "\n")
	totalLines := len(lines)

	// Apply offset/limit
	startLine := 0
	if offset, ok := getFloat(args, "offset"); ok && offset > 0 {
		startLine = int(offset) - 1 // 1-based to 0-based
		if startLine >= len(lines) {
			startLine = len(lines)
		}
	}
	endLine := len(lines)
	if limit, ok := getFloat(args, "limit"); ok && limit > 0 {
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

	// Check if file already exists
	_, existErr := os.Stat(absPath)
	existed := existErr == nil

	// Ensure parent directory exists
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Sprintf("Error creating directory: %v", err), false
	}

	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	lines := strings.Count(content, "\n") + 1
	action := "Created"
	if existed {
		action = "Updated"
	}
	return fmt.Sprintf("%s %s (%d lines, %d bytes)", action, relPath, lines, len(content)), true
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
		if os.IsNotExist(err) {
			return fmt.Sprintf("Error: File not found: %s", relPath), false
		}
		return fmt.Sprintf("Error: %v", err), false
	}

	content := string(data)
	occurrences := strings.Count(content, oldText)

	if occurrences == 0 {
		// Show a preview to help the model correct its match
		lines := strings.Split(content, "\n")
		var preview string
		if len(lines) <= 30 {
			preview = content
		} else {
			preview = strings.Join(lines[:15], "\n") + "\n...\n" + strings.Join(lines[len(lines)-15:], "\n")
		}
		return truncateOutput(fmt.Sprintf(
			"Error: old_text not found in %s.\n\n"+
				"The text you're looking for doesn't match the file contents exactly. "+
				"Make sure whitespace and indentation match precisely.\n\n"+
				"File preview:\n%s", relPath, preview)), false
	}

	if occurrences > 1 {
		return fmt.Sprintf("Error: old_text found %d times in %s. Include more surrounding context to make the match unique.", occurrences, relPath), false
	}

	newContent := strings.Replace(content, oldText, newText, 1)
	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		return fmt.Sprintf("Error writing: %v", err), false
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

	return fmt.Sprintf("Edited %s: replaced %d line(s) with %d line(s)%s", relPath, oldLines, newLines, diffNote), true
}

// ── multi_edit ───────────────────────────────────────────────

type editEntry struct {
	Path       string `json:"path"`
	OldText    string `json:"old_text"`
	NewText    string `json:"new_text"`
	ReplaceAll bool   `json:"replace_all"`
}

func toolMultiEdit(args map[string]interface{}, workDir string) (string, bool) {
	editsRaw, ok := args["edits"]
	if !ok {
		return "Error: \"edits\" parameter is required.", false
	}

	// Re-marshal and unmarshal to get typed struct
	editsJSON, err := json.Marshal(editsRaw)
	if err != nil {
		return fmt.Sprintf("Error: could not parse edits: %v", err), false
	}

	var edits []editEntry
	if err := json.Unmarshal(editsJSON, &edits); err != nil {
		return fmt.Sprintf("Error: could not parse edits array: %v", err), false
	}

	if len(edits) == 0 {
		return "Error: \"edits\" must be a non-empty array.", false
	}

	// Validate all entries upfront
	for i, e := range edits {
		if e.Path == "" {
			return fmt.Sprintf("Error: edit[%d] missing \"path\".", i), false
		}
		if e.OldText == "" {
			return fmt.Sprintf("Error: edit[%d] missing \"old_text\".", i), false
		}
	}

	// Group edits by file path, preserving order
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
		status string // "ok", "error", "skipped"
		detail string
	}
	results := make([]editResult, len(edits))
	var filesModified []string
	totalOk, totalFailed := 0, 0

	for _, filePath := range fileOrder {
		fileEdits := byFile[filePath]

		absPath, err := safePath(workDir, filePath)
		if err != nil {
			for _, e := range fileEdits {
				results[e.index] = editResult{path: filePath, status: "error", detail: err.Error()}
				totalFailed++
			}
			continue
		}

		data, err := os.ReadFile(absPath)
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

		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			for _, e := range fileEdits {
				results[e.index] = editResult{path: filePath, status: "error", detail: fmt.Sprintf("Write failed: %v", err)}
				if results[e.index].status == "ok" {
					totalOk--
				}
				totalFailed++
			}
			continue
		}
		filesModified = append(filesModified, filePath)
	}

	// Build report
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

	return truncateOutput(sb.String()), totalFailed == 0
}

// ── list_files ───────────────────────────────────────────────

// ignoreDirs contains directory names to skip during listing/globbing.
var ignoreDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"__pycache__": true, "dist": true, ".next": true,
	"build": true, ".cache": true, ".idea": true,
	".vscode": true, "venv": true, ".venv": true,
}

func toolListFiles(args map[string]interface{}, workDir string) (string, bool) {
	dirPath := getString(args, "path")
	if dirPath == "" {
		dirPath = "."
	}
	pattern := getString(args, "pattern")

	fullPath, err := safePath(workDir, dirPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	if pattern != "" {
		// Glob search — use filepath.WalkDir with manual matching
		var matches []string
		globPattern := filepath.Join(fullPath, pattern)

		// First try simple glob
		simpleMatches, err := filepath.Glob(globPattern)
		if err == nil && len(simpleMatches) > 0 {
			absRoot, _ := filepath.Abs(workDir)
			for _, m := range simpleMatches {
				rel, _ := filepath.Rel(absRoot, m)
				matches = append(matches, rel)
			}
		} else {
			// Walk for ** patterns (filepath.Glob doesn't support **)
			absRoot, _ := filepath.Abs(workDir)
			filepath.WalkDir(fullPath, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if ignoreDirs[d.Name()] || (d.Name() != "." && strings.HasPrefix(d.Name(), ".")) {
						return filepath.SkipDir
					}
					return nil
				}
				rel, _ := filepath.Rel(absRoot, path)
				// Match just the filename against the pattern (strip **/ prefix)
				basePat := pattern
				if strings.HasPrefix(basePat, "**/") {
					basePat = basePat[3:]
				}
				if matched, _ := filepath.Match(basePat, d.Name()); matched {
					matches = append(matches, rel)
				}
				return nil
			})
		}

		if len(matches) == 0 {
			return fmt.Sprintf("No files matching %q in %s", pattern, dirPath), true
		}

		sort.Strings(matches)
		shown := matches
		extra := ""
		if len(matches) > maxResultLines {
			shown = matches[:maxResultLines]
			extra = fmt.Sprintf("\n\n--- %d more files not shown ---", len(matches)-maxResultLines)
		}

		return fmt.Sprintf("%d files matching %q in %s:\n\n%s%s", len(matches), pattern, dirPath, strings.Join(shown, "\n"), extra), true
	}

	// Simple directory listing
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("Error: Directory not found: %s", dirPath), false
		}
		return fmt.Sprintf("Error listing %s: %v", dirPath, err), false
	}

	// Sort: directories first, then files
	sort.Slice(entries, func(i, j int) bool {
		iDir := entries[i].IsDir()
		jDir := entries[j].IsDir()
		if iDir != jDir {
			return iDir
		}
		return entries[i].Name() < entries[j].Name()
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "Contents of %s (%d entries):\n\n", dirPath, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			fmt.Fprintf(&sb, "  [dir]  %s\n", e.Name())
		} else {
			fmt.Fprintf(&sb, "  [file] %s\n", e.Name())
		}
	}

	return sb.String(), true
}

// ── search_files ─────────────────────────────────────────────

func toolSearchFiles(args map[string]interface{}, workDir string) (string, bool) {
	pattern := getString(args, "pattern")
	if pattern == "" {
		return "Error: \"pattern\" parameter is required.", false
	}

	dirPath := getString(args, "path")
	if dirPath == "" {
		dirPath = "."
	}
	include := getString(args, "include")

	fullPath, err := safePath(workDir, dirPath)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), false
	}

	// Try ripgrep first, fall back to grep
	output, err := searchWithRg(pattern, fullPath, include, workDir)
	if err != nil {
		// rg not found — try grep
		output, err = searchWithGrep(pattern, fullPath, include, workDir)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), false
		}
	}

	if output == "" {
		return fmt.Sprintf("No matches for %q in %s", pattern, dirPath), true
	}

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) > maxResultLines {
		shown := strings.Join(lines[:maxResultLines], "\n")
		return truncateOutput(fmt.Sprintf("%d matches for %q in %s (showing first %d):\n\n%s", len(lines), pattern, dirPath, maxResultLines, shown)), true
	}

	return truncateOutput(fmt.Sprintf("%d matches for %q in %s:\n\n%s", len(lines), pattern, dirPath, output)), true
}

func searchWithRg(pattern, searchPath, include, workDir string) (string, error) {
	args := []string{
		"rg",
		"--line-number",
		"--no-heading",
		"--color=never",
		"--max-count=100",
		"--max-filesize=1M",
		"--glob=!node_modules",
		"--glob=!.git",
		"--glob=!dist",
		"--glob=!.next",
		"--glob=!build",
		"--glob=!*.min.js",
		"--glob=!*.min.css",
		"--glob=!package-lock.json",
		"--glob=!yarn.lock",
		"--glob=!go.sum",
	}

	if include != "" {
		args = append(args, "--glob", include)
	}

	args = append(args, "--", pattern, searchPath)

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()

	if err != nil {
		// Exit code 1 = no matches (not an error)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		// rg not found
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 127 {
			return "", fmt.Errorf("rg not found")
		}
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such file") {
			return "", fmt.Errorf("rg not found")
		}
		return "", err
	}

	// Make paths relative to project root
	absRoot, _ := filepath.Abs(workDir)
	output := strings.ReplaceAll(string(out), absRoot+"/", "")
	output = strings.ReplaceAll(output, absRoot+string(filepath.Separator), "")

	return strings.TrimRight(output, "\n"), nil
}

func searchWithGrep(pattern, searchPath, include, workDir string) (string, error) {
	incFlag := "*"
	if include != "" {
		incFlag = include
	}

	cmd := exec.Command("grep", "-rn", "--include="+incFlag, pattern, searchPath)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil // no matches
		}
		return "", err
	}

	absRoot, _ := filepath.Abs(workDir)
	output := strings.ReplaceAll(string(out), absRoot+"/", "")
	output = strings.ReplaceAll(output, absRoot+string(filepath.Separator), "")

	return strings.TrimRight(output, "\n"), nil
}

// ── web_search ───────────────────────────────────────────────

func toolWebSearch(args map[string]interface{}) (string, bool) {
	query := getString(args, "query")
	if query == "" {
		return "Error: query is required", false
	}
	maxResults := 5
	if mr, ok := getFloat(args, "max_results"); ok && mr > 0 {
		maxResults = int(mr)
	}

	resp, err := WebSearch(query, maxResults)
	if err != nil {
		return fmt.Sprintf("Search error: %v", err), false
	}

	return FormatSearchResults(resp), true
}

// ── shell ────────────────────────────────────────────────────

func toolShell(args map[string]interface{}, workDir string) (string, bool) {
	command := getString(args, "command")
	if command == "" {
		return "Error: command is required", false
	}

	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"TERM=dumb",
		"NO_COLOR=1",
		"FORCE_COLOR=0",
		"CI=1",
	)

	// Combine stdout + stderr, timeout at 2 minutes
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
			return truncateOutput(fmt.Sprintf("%s\n\nExit code: %s | Wall time: %.1fs", result, cmdErr.Error(), elapsed)), false
		}
		return truncateOutput(fmt.Sprintf("%s\n\nExit code: 0 | Wall time: %.1fs", result, elapsed)), true
	case <-time.After(2 * time.Minute):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return "Error: command timed out after 2 minutes", false
	}
}
