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

// Glob is a fast file pattern matching tool that returns paths sorted
// by modification time. Dedicated to file discovery — no content search.
type Glob struct{}

func (Glob) Name() string                          { return "glob" }
func (Glob) ConcurrencySafe(_ map[string]any) bool { return true }
func (Glob) Effects() []tool.Effect                { return []tool.Effect{tool.EffectReadsFS} }

func (Glob) Description() string {
	return "Fast file pattern matching. Returns matching file paths sorted by modification time (newest first). " +
		"Use this to find files by name patterns (e.g. \"**/*.go\", \"src/**/*.ts\"). " +
		"For searching file contents, use grep instead."
}

func (Glob) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"pattern": {
				"type": "string",
				"description": "Glob pattern to match files (e.g. \"**/*.go\", \"src/**/*.ts\", \"*.json\")."
			},
			"path": {
				"type": "string",
				"description": "Directory to search in. Defaults to project root."
			}
		},
		"required": ["pattern"]
	}`)
}

const globMaxResults = 100

func (Glob) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return tool.Result{Output: "Error: pattern is required", Success: false}
	}

	searchDir, _ := args["path"].(string)
	if searchDir == "" {
		searchDir = "."
	}

	fullPath, err := SafePath(env.WorkDir, searchDir)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: %v", err), Success: false}
	}

	absRoot, _ := filepath.Abs(env.WorkDir)

	type fileEntry struct {
		relPath string
		modTime int64
	}
	var matches []fileEntry

	// Walk and match
	filepath.WalkDir(fullPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if ignoreDirs[name] || (name != "." && strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}

		rel, _ := filepath.Rel(absRoot, path)

		// Match against the pattern
		matched := false

		// Try full relative path match first
		if m, _ := filepath.Match(pattern, rel); m {
			matched = true
		}

		// For ** patterns, match just the filename against the base pattern
		if !matched && strings.Contains(pattern, "**") {
			basePat := pattern
			// Strip leading **/ prefixes
			for strings.HasPrefix(basePat, "**/") {
				basePat = basePat[3:]
			}
			if m, _ := filepath.Match(basePat, d.Name()); m {
				matched = true
			}
		}

		// Simple glob match against filename
		if !matched {
			if m, _ := filepath.Match(pattern, d.Name()); m {
				matched = true
			}
		}

		if matched {
			info, err := d.Info()
			modTime := int64(0)
			if err == nil {
				modTime = info.ModTime().UnixNano()
			}
			matches = append(matches, fileEntry{relPath: rel, modTime: modTime})
		}
		return nil
	})

	if len(matches) == 0 {
		return tool.Result{Output: fmt.Sprintf("No files matching %q", pattern), Success: true}
	}

	// Sort by modification time (newest first), tiebreak by name
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].modTime != matches[j].modTime {
			return matches[i].modTime > matches[j].modTime
		}
		return matches[i].relPath < matches[j].relPath
	})

	truncated := false
	total := len(matches)
	if len(matches) > globMaxResults {
		matches = matches[:globMaxResults]
		truncated = true
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d files matching %q", total, pattern)
	if truncated {
		fmt.Fprintf(&sb, " (showing %d of %d — refine your pattern)", globMaxResults, total)
	}
	sb.WriteString(":\n\n")
	for _, m := range matches {
		sb.WriteString(m.relPath)
		sb.WriteByte('\n')
	}

	return tool.Result{Output: sb.String(), Success: true}
}
