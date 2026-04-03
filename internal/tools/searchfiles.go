package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

type SearchFiles struct{}

func (SearchFiles) Name() string                          { return "search_files" }
func (SearchFiles) ConcurrencySafe(_ map[string]any) bool { return true }
func (SearchFiles) Effects() []tool.Effect                { return []tool.Effect{tool.EffectReadsFS} }

func (SearchFiles) Description() string {
	return "Regex search across files in the project (powered by ripgrep). " +
		"Use for finding definitions, usages, patterns, and text across the codebase."
}

func (SearchFiles) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Regex pattern to search for."
			},
			"path": {
				"type": "string",
				"description": "Directory to search in, relative to project root. Defaults to root."
			},
			"include": {
				"type": "string",
				"description": "Glob filter for files to search (e.g. \"*.go\", \"*.ts\")."
			},
			"context_lines": {
				"type": "number",
				"description": "Number of context lines to show around matches (0-10)."
			}
		},
		"required": ["pattern"]
	}`)
}

func (SearchFiles) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return tool.Result{Output: "Error: \"pattern\" parameter is required.", Success: false}
	}

	dirPath, _ := args["path"].(string)
	if dirPath == "" {
		dirPath = "."
	}
	include, _ := args["include"].(string)
	contextLines := 0
	if cl, ok := args["context_lines"].(float64); ok && cl > 0 {
		contextLines = int(cl)
		if contextLines > 10 {
			contextLines = 10
		}
	}

	fullPath, err := SafePath(env.WorkDir, dirPath)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	output, err := searchWithRg(pattern, fullPath, include, env.WorkDir, contextLines)
	if err != nil {
		output, err = searchWithGrep(pattern, fullPath, include, env.WorkDir, contextLines)
		if err != nil {
			return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
		}
	}

	if output == "" {
		return tool.Result{Output: fmt.Sprintf("No matches for %q in %s", pattern, dirPath), Success: true}
	}

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) > maxResultLines {
		shown := strings.Join(lines[:maxResultLines], "\n")
		return tool.Result{
			Output:  TruncateOutput(fmt.Sprintf("%d matches for %q in %s (showing first %d):\n\n%s", len(lines), pattern, dirPath, maxResultLines, shown), maxOutputChars),
			Success: true,
		}
	}

	return tool.Result{
		Output:  TruncateOutput(fmt.Sprintf("%d matches for %q in %s:\n\n%s", len(lines), pattern, dirPath, output), maxOutputChars),
		Success: true,
	}
}

func searchWithRg(pattern, searchPath, include, workDir string, contextLines int) (string, error) {
	args := []string{
		"rg", "--line-number", "--no-heading", "--color=never",
		"--max-count=100", "--max-filesize=1M",
		"--glob=!node_modules", "--glob=!.git", "--glob=!dist",
		"--glob=!.next", "--glob=!build", "--glob=!*.min.js",
		"--glob=!*.min.css", "--glob=!package-lock.json",
		"--glob=!yarn.lock", "--glob=!go.sum",
	}
	if include != "" {
		args = append(args, "--glob", include)
	}
	if contextLines > 0 {
		args = append(args, fmt.Sprintf("-C%d", contextLines))
	}
	args = append(args, "--", pattern, searchPath)

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such file") {
			return "", fmt.Errorf("rg not found")
		}
		return "", err
	}

	absRoot, _ := filepath.Abs(workDir)
	output := strings.ReplaceAll(string(out), absRoot+"/", "")
	output = strings.ReplaceAll(output, absRoot+string(filepath.Separator), "")
	return strings.TrimRight(output, "\n"), nil
}

func searchWithGrep(pattern, searchPath, include, workDir string, contextLines int) (string, error) {
	incFlag := "*"
	if include != "" {
		incFlag = include
	}
	grepArgs := []string{"-rn", "--include=" + incFlag}
	if contextLines > 0 {
		grepArgs = append(grepArgs, fmt.Sprintf("-C%d", contextLines))
	}
	grepArgs = append(grepArgs, pattern, searchPath)
	cmd := exec.Command("grep", grepArgs...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	absRoot, _ := filepath.Abs(workDir)
	output := strings.ReplaceAll(string(out), absRoot+"/", "")
	output = strings.ReplaceAll(output, absRoot+string(filepath.Separator), "")
	return strings.TrimRight(output, "\n"), nil
}
