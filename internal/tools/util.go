package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SafePath resolves a relative path within workDir and ensures it
// doesn't escape via traversal. Checks both the resolved path and
// symlink target for containment within the project root.
func SafePath(workDir, relPath string) (string, error) {
	resolved := filepath.Join(workDir, relPath)
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	absRoot, _ := filepath.Abs(workDir)
	if !strings.HasPrefix(abs, absRoot+string(filepath.Separator)) && abs != absRoot {
		return "", fmt.Errorf("path %q resolves outside project root", relPath)
	}
	// Resolve symlinks and re-check containment
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		realRoot, _ := filepath.EvalSymlinks(absRoot)
		if !strings.HasPrefix(real, realRoot+string(filepath.Separator)) && real != realRoot {
			return "", fmt.Errorf("path %q symlinks outside project root", relPath)
		}
		abs = real
	}
	return abs, nil
}

// TruncateOutput caps tool output at maxChars with a truncation notice.
func TruncateOutput(s string, maxChars int) string {
	if len(s) > maxChars {
		cutChars := len(s) - maxChars
		return s[:maxChars] + fmt.Sprintf("\n\n--- OUTPUT TRUNCATED (%d chars cut, %d total) ---\nRefine your command or use offset/limit to see specific sections.", cutChars, len(s))
	}
	return s
}
