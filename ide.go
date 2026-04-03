package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ──────────────────────────────────────────────────────────────
//  IDE Bridge — discover and connect to VS Code / JetBrains
//
//  Claude Code's VS Code extension writes lockfiles to
//  ~/.claude/ide/{port}.lock. We read these to discover running
//  IDE instances and connect via MCP over WebSocket.
//
//  This lets us:
//  - Open files in the IDE when the agent reads/edits them
//  - Show diffs in the IDE's diff viewer
//  - Delegate permission prompts to the IDE (future)
//
//  We also support our own lockfile format at ~/.codebase/ide/
//  for future codebase-cli VS Code extension.
// ──────────────────────────────────────────────────────────────

// IDELockfile represents a running IDE instance.
type IDELockfile struct {
	WorkspaceFolders []string `json:"workspaceFolders"`
	Port             int      `json:"port"`
	PID              int      `json:"pid"`
	IDEName          string   `json:"ideName"`
	Transport        string   `json:"transport"` // "ws" or "sse"
	AuthToken        string   `json:"authToken,omitempty"`
	RunningInWindows bool     `json:"runningInWindows,omitempty"`
}

// IDEConnection represents a connected IDE.
type IDEConnection struct {
	Lockfile IDELockfile
	FilePath string // path to the lockfile
}

// DiscoverIDE looks for running IDE instances that match the given working directory.
// Searches both Claude Code's lockfile location and our own.
func DiscoverIDE(workDir string) *IDEConnection {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	// Search directories (Claude Code's + ours)
	searchDirs := []string{
		filepath.Join(home, ".claude", "ide"),
		filepath.Join(home, ".codebase", "ide"),
	}

	type candidate struct {
		lockfile IDELockfile
		path     string
		modTime  int64
	}
	var candidates []candidate

	for _, dir := range searchDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".lock") {
				continue
			}

			path := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			var lf IDELockfile
			if err := json.Unmarshal(data, &lf); err != nil {
				continue
			}

			// Check if this IDE's workspace matches our working directory
			if matchesWorkspace(lf.WorkspaceFolders, workDir) {
				info, _ := entry.Info()
				modTime := int64(0)
				if info != nil {
					modTime = info.ModTime().UnixNano()
				}
				candidates = append(candidates, candidate{
					lockfile: lf,
					path:     path,
					modTime:  modTime,
				})
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Pick the most recently modified lockfile
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].modTime > candidates[j].modTime
	})

	best := candidates[0]

	// Verify the IDE process is still alive
	if !isProcessAlive(best.lockfile.PID) {
		// Stale lockfile — clean up
		os.Remove(best.path)
		return nil
	}

	return &IDEConnection{
		Lockfile: best.lockfile,
		FilePath: best.path,
	}
}

// matchesWorkspace checks if any workspace folder contains or equals the working directory.
func matchesWorkspace(folders []string, workDir string) bool {
	absWorkDir, _ := filepath.Abs(workDir)
	for _, folder := range folders {
		absFolder, _ := filepath.Abs(folder)
		if absWorkDir == absFolder || strings.HasPrefix(absWorkDir, absFolder+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// isProcessAlive checks if a process with the given PID exists.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Send signal 0 to check.
	err = proc.Signal(os.Signal(nil))
	// If the process exists, Signal returns nil or a permission error
	// If it doesn't exist, it returns a "no such process" error
	return err == nil || !strings.Contains(err.Error(), "no such process")
}

// IDEInfo returns a human-readable description of the connected IDE.
func (c *IDEConnection) IDEInfo() string {
	name := c.Lockfile.IDEName
	if name == "" {
		name = "unknown IDE"
	}
	return fmt.Sprintf("%s (port %d, PID %d)", name, c.Lockfile.Port, c.Lockfile.PID)
}
