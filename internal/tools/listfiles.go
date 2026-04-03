package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

const maxResultLines = 500

// ignoreDirs contains directory names to skip during listing/globbing.
var ignoreDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"__pycache__": true, "dist": true, ".next": true,
	"build": true, ".cache": true, ".idea": true,
	".vscode": true, "venv": true, ".venv": true,
}

type ListFiles struct{}

func (ListFiles) Name() string                          { return "list_files" }
func (ListFiles) ConcurrencySafe(_ map[string]any) bool { return true }
func (ListFiles) Effects() []tool.Effect                { return []tool.Effect{tool.EffectReadsFS} }

func (ListFiles) Description() string {
	return "List directory contents or search for files using glob patterns. " +
		"Without a pattern, lists the direct contents of a directory. " +
		"With a pattern (e.g. \"**/*.go\"), searches recursively for matching files."
}

func (ListFiles) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"path": {
				"type": "string",
				"description": "Directory path relative to project root. Defaults to root."
			},
			"pattern": {
				"type": "string",
				"description": "Glob pattern to search for (e.g. \"**/*.go\", \"*.ts\")."
			}
		}
	}`)
}

func (ListFiles) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	dirPath, _ := args["path"].(string)
	if dirPath == "" {
		dirPath = "."
	}
	pattern, _ := args["pattern"].(string)

	fullPath, err := SafePath(env.WorkDir, dirPath)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	if pattern != "" {
		return listFilesGlob(fullPath, pattern, dirPath, env.WorkDir)
	}
	return listFilesDir(fullPath, dirPath)
}

func listFilesGlob(fullPath, pattern, dirPath, workDir string) tool.Result {
	var matches []string
	globPattern := filepath.Join(fullPath, pattern)

	simpleMatches, err := filepath.Glob(globPattern)
	if err == nil && len(simpleMatches) > 0 {
		absRoot, _ := filepath.Abs(workDir)
		for _, m := range simpleMatches {
			rel, _ := filepath.Rel(absRoot, m)
			matches = append(matches, rel)
		}
	} else {
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
		return tool.Result{Output: fmt.Sprintf("No files matching %q in %s", pattern, dirPath), Success: true}
	}

	sort.Strings(matches)
	shown := matches
	extra := ""
	if len(matches) > maxResultLines {
		shown = matches[:maxResultLines]
		extra = fmt.Sprintf("\n\n--- %d more files not shown ---", len(matches)-maxResultLines)
	}

	return tool.Result{
		Output:  fmt.Sprintf("%d files matching %q in %s:\n\n%s%s", len(matches), pattern, dirPath, strings.Join(shown, "\n"), extra),
		Success: true,
	}
}

func listFilesDir(fullPath, dirPath string) tool.Result {
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return tool.Result{Output: fmt.Sprintf("Error: Directory not found: %s", dirPath), Success: false}
		}
		return tool.Result{Output: fmt.Sprintf("Error listing %s: %v", dirPath, err), Success: false}
	}

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

	return tool.Result{Output: sb.String(), Success: true}
}
