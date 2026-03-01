package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ──────────────────────────────────────────────────────────────
//  Session Persistence
//
//  Saves conversation history to ~/.codebase/sessions/ so users
//  can quit and resume. Sessions are keyed by working directory.
// ──────────────────────────────────────────────────────────────

const maxSessionAge = 7 * 24 * time.Hour // 7 days

type SessionData struct {
	WorkDir   string        `json:"work_dir"`
	Model     string        `json:"model"`
	History   []ChatMessage `json:"history"`
	Tokens    TokenUsage    `json:"tokens"`
	Files     int           `json:"files"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// sessionsDir returns the path to the sessions directory, creating it if needed.
func sessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".codebase", "sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// sessionFile returns the session file path for a given working directory.
func sessionFile(workDir string) (string, error) {
	dir, err := sessionsDir()
	if err != nil {
		return "", err
	}
	// Hash the workdir to get a stable filename
	h := sha256.Sum256([]byte(workDir))
	name := fmt.Sprintf("%x.json", h[:8])
	return filepath.Join(dir, name), nil
}

// SaveSession persists the agent's conversation to disk.
func SaveSession(agent *Agent, tokens TokenUsage) error {
	if agent == nil || len(agent.history) <= 1 {
		return nil // nothing to save (just system prompt)
	}

	path, err := sessionFile(agent.workDir)
	if err != nil {
		return err
	}

	data := SessionData{
		WorkDir:   agent.workDir,
		Model:     agent.client.Model,
		History:   agent.history,
		Tokens:    tokens,
		Files:     agent.files,
		UpdatedAt: time.Now(),
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: write to temp file, then rename
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, jsonData, 0600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// LoadSession restores a previous conversation for the given working directory.
// Returns nil if no session exists or it's too old.
func LoadSession(workDir, model string) *SessionData {
	path, err := sessionFile(workDir)
	if err != nil {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var session SessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return nil
	}

	// Check age
	if time.Since(session.UpdatedAt) > maxSessionAge {
		os.Remove(path) // clean up stale session
		return nil
	}

	// Check model match (different model = different conversation)
	if session.Model != model {
		return nil
	}

	// Check workdir match
	if session.WorkDir != workDir {
		return nil
	}

	return &session
}

// ClearSession removes the session file for a working directory.
func ClearSession(workDir string) error {
	path, err := sessionFile(workDir)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// CleanStaleSessions removes sessions older than maxSessionAge.
func CleanStaleSessions() {
	dir, err := sessionsDir()
	if err != nil {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var session SessionData
		if err := json.Unmarshal(data, &session); err != nil {
			os.Remove(path) // corrupt, remove
			continue
		}
		if time.Since(session.UpdatedAt) > maxSessionAge {
			os.Remove(path)
		}
	}
}
