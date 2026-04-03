package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

// Grep is a powerful ripgrep-based search tool with output modes,
// multiline support, pagination, and type filtering.
// This matches and exceeds CC's GrepTool capabilities.
type Grep struct{}

func (Grep) Name() string                          { return "grep" }
func (Grep) ConcurrencySafe(_ map[string]any) bool { return true }
func (Grep) Effects() []tool.Effect                { return []tool.Effect{tool.EffectReadsFS} }

func (Grep) Description() string {
	return "Search file contents using regex patterns (powered by ripgrep). " +
		"Supports three output modes: 'content' shows matching lines with context, " +
		"'files_with_matches' shows only file paths (default), 'count' shows match counts per file. " +
		"Use glob or type parameters to filter files. Supports multiline patterns."
}

func (Grep) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Regex pattern to search for."
			},
			"path": {
				"type": "string",
				"description": "File or directory to search in. Defaults to project root."
			},
			"glob": {
				"type": "string",
				"description": "Glob pattern to filter files (e.g. \"*.js\", \"*.{ts,tsx}\")."
			},
			"type": {
				"type": "string",
				"description": "File type filter (rg --type). Common: js, py, go, rust, java, ts, css, html."
			},
			"output_mode": {
				"type": "string",
				"description": "Output mode: 'content' shows matching lines, 'files_with_matches' shows file paths (default), 'count' shows match counts.",
				"enum": ["content", "files_with_matches", "count"]
			},
			"-A": {
				"type": "number",
				"description": "Lines to show after each match. Requires output_mode: content."
			},
			"-B": {
				"type": "number",
				"description": "Lines to show before each match. Requires output_mode: content."
			},
			"-C": {
				"type": "number",
				"description": "Lines to show before and after each match (context). Requires output_mode: content."
			},
			"-i": {
				"type": "boolean",
				"description": "Case insensitive search."
			},
			"-n": {
				"type": "boolean",
				"description": "Show line numbers. Defaults to true for content mode."
			},
			"multiline": {
				"type": "boolean",
				"description": "Enable multiline matching where . matches newlines."
			},
			"head_limit": {
				"type": "number",
				"description": "Limit output to first N lines/entries. Default 250. Pass 0 for unlimited."
			},
			"offset": {
				"type": "number",
				"description": "Skip first N lines/entries before applying head_limit."
			}
		},
		"required": ["pattern"]
	}`)
}

func (Grep) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return tool.Result{Output: "Error: pattern is required", Success: false}
	}

	searchPath, _ := args["path"].(string)
	if searchPath == "" {
		searchPath = "."
	}
	fullPath, err := SafePath(env.WorkDir, searchPath)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	outputMode, _ := args["output_mode"].(string)
	if outputMode == "" {
		outputMode = "files_with_matches"
	}

	headLimit := 250
	if hl, ok := args["head_limit"].(float64); ok {
		headLimit = int(hl)
	}
	offset := 0
	if off, ok := args["offset"].(float64); ok {
		offset = int(off)
	}

	// Build ripgrep command
	rgArgs := []string{
		"--color=never",
		"--max-columns=500",
		"--hidden",
		"--glob=!.git", "--glob=!.svn", "--glob=!.hg",
		"--glob=!node_modules", "--glob=!dist", "--glob=!build",
		"--glob=!.next", "--glob=!*.min.js", "--glob=!*.min.css",
		"--glob=!package-lock.json", "--glob=!yarn.lock", "--glob=!go.sum",
	}

	switch outputMode {
	case "files_with_matches":
		rgArgs = append(rgArgs, "--files-with-matches")
	case "count":
		rgArgs = append(rgArgs, "--count")
	case "content":
		// Show line numbers by default in content mode
		showLineNumbers := true
		if n, ok := args["-n"].(bool); ok {
			showLineNumbers = n
		}
		if showLineNumbers {
			rgArgs = append(rgArgs, "--line-number")
		}
		rgArgs = append(rgArgs, "--no-heading")

		// Context lines
		if c, ok := args["-C"].(float64); ok && c > 0 {
			rgArgs = append(rgArgs, fmt.Sprintf("-C%d", int(c)))
		} else {
			if a, ok := args["-A"].(float64); ok && a > 0 {
				rgArgs = append(rgArgs, fmt.Sprintf("-A%d", int(a)))
			}
			if b, ok := args["-B"].(float64); ok && b > 0 {
				rgArgs = append(rgArgs, fmt.Sprintf("-B%d", int(b)))
			}
		}
	}

	// Case insensitive
	if ci, ok := args["-i"].(bool); ok && ci {
		rgArgs = append(rgArgs, "-i")
	}

	// File type filter
	if typ, ok := args["type"].(string); ok && typ != "" {
		rgArgs = append(rgArgs, "--type", typ)
	}

	// Glob filter
	if glob, ok := args["glob"].(string); ok && glob != "" {
		rgArgs = append(rgArgs, "--glob", glob)
	}

	// Multiline
	if ml, ok := args["multiline"].(bool); ok && ml {
		rgArgs = append(rgArgs, "-U", "--multiline-dotall")
	}

	// Pattern (use -e if it starts with -)
	if strings.HasPrefix(pattern, "-") {
		rgArgs = append(rgArgs, "-e", pattern)
	} else {
		rgArgs = append(rgArgs, "--", pattern)
	}
	rgArgs = append(rgArgs, fullPath)

	cmd := exec.Command("rg", rgArgs...)
	cmd.Dir = env.WorkDir
	out, err := cmd.CombinedOutput()

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 {
				return tool.Result{Output: fmt.Sprintf("No matches for %q", pattern), Success: true}
			}
			if exitErr.ExitCode() == 2 {
				return tool.Result{Output: fmt.Sprintf("Error: %s", strings.TrimSpace(string(out))), Success: false}
			}
		}
		// rg not found — fall back to basic grep
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such file") {
			return fallbackGrep(pattern, fullPath, env.WorkDir, outputMode, headLimit, offset)
		}
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	// Relativize paths
	absRoot, _ := filepath.Abs(env.WorkDir)
	output := strings.ReplaceAll(string(out), absRoot+"/", "")
	output = strings.ReplaceAll(output, absRoot+string(filepath.Separator), "")
	output = strings.TrimRight(output, "\n")

	if output == "" {
		return tool.Result{Output: fmt.Sprintf("No matches for %q", pattern), Success: true}
	}

	// Apply offset and head_limit
	lines := strings.Split(output, "\n")
	totalLines := len(lines)

	if offset > 0 {
		if offset >= len(lines) {
			return tool.Result{Output: fmt.Sprintf("No results (offset %d exceeds %d total)", offset, totalLines), Success: true}
		}
		lines = lines[offset:]
	}

	truncated := false
	if headLimit > 0 && len(lines) > headLimit {
		lines = lines[:headLimit]
		truncated = true
	}

	var sb strings.Builder
	switch outputMode {
	case "files_with_matches":
		sb.WriteString(fmt.Sprintf("%d files matching %q", totalLines, pattern))
	case "count":
		sb.WriteString(fmt.Sprintf("Match counts for %q", pattern))
	case "content":
		sb.WriteString(fmt.Sprintf("Matches for %q", pattern))
	}

	if offset > 0 {
		sb.WriteString(fmt.Sprintf(" (offset %d)", offset))
	}
	if truncated {
		sb.WriteString(fmt.Sprintf(" (showing %d of %d)", len(lines), totalLines))
	}
	sb.WriteString(":\n\n")
	sb.WriteString(strings.Join(lines, "\n"))

	return tool.Result{Output: TruncateOutput(sb.String(), maxOutputChars), Success: true}
}

func fallbackGrep(pattern, searchPath, workDir, outputMode string, headLimit, offset int) tool.Result {
	grepArgs := []string{"-rn"}
	if outputMode == "files_with_matches" {
		grepArgs = []string{"-rl"}
	} else if outputMode == "count" {
		grepArgs = []string{"-rc"}
	}
	grepArgs = append(grepArgs, pattern, searchPath)

	cmd := exec.Command("grep", grepArgs...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return tool.Result{Output: fmt.Sprintf("No matches for %q", pattern), Success: true}
		}
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	absRoot, _ := filepath.Abs(workDir)
	output := strings.ReplaceAll(string(out), absRoot+"/", "")
	output = strings.TrimRight(output, "\n")

	lines := strings.Split(output, "\n")
	if offset > 0 && offset < len(lines) {
		lines = lines[offset:]
	}
	if headLimit > 0 && len(lines) > headLimit {
		lines = lines[:headLimit]
	}

	return tool.Result{
		Output:  TruncateOutput(strconv.Itoa(len(lines))+" results:\n\n"+strings.Join(lines, "\n"), maxOutputChars),
		Success: true,
	}
}
