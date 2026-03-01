package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Set by goreleaser ldflags
var (
	version = "dev"
	commit  = "none"
)

// ──────────────────────────────────────────────────────────────
//  Configuration
// ──────────────────────────────────────────────────────────────

type Config struct {
	APIKey  string
	BaseURL string
	Model   string
	WorkDir string
	Resume  bool // --resume flag: restore previous session
}

func loadConfig() (*Config, error) {
	// CLI flags
	model := flag.String("model", "", "LLM model name (default: gpt-4o)")
	dir := flag.String("dir", "", "Working directory (default: current dir)")
	baseURL := flag.String("base-url", "", "OpenAI-compatible API base URL")
	resume := flag.Bool("resume", false, "Resume previous session for this directory")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("codebase %s (%s)\n", version, commit)
		os.Exit(0)
	}

	cfg := &Config{}

	// API key: env var → saved config → interactive prompt
	cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	if cfg.APIKey == "" {
		saved := loadSavedConfig()
		cfg.APIKey = saved.APIKey
	}
	if cfg.APIKey == "" {
		key, err := promptForAPIKey()
		if err != nil {
			return nil, err
		}
		cfg.APIKey = key
	}

	// Base URL
	cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
	if *baseURL != "" {
		cfg.BaseURL = *baseURL
	}
	if cfg.BaseURL == "" {
		saved := loadSavedConfig()
		if saved.BaseURL != "" {
			cfg.BaseURL = saved.BaseURL
		} else {
			cfg.BaseURL = "https://api.openai.com/v1"
		}
	}

	// Model
	cfg.Model = os.Getenv("OPENAI_MODEL")
	if *model != "" {
		cfg.Model = *model
	}
	if cfg.Model == "" {
		saved := loadSavedConfig()
		if saved.Model != "" {
			cfg.Model = saved.Model
		} else {
			cfg.Model = "gpt-4o"
		}
	}

	// Working directory
	if *dir != "" {
		abs, err := filepath.Abs(*dir)
		if err != nil {
			return nil, fmt.Errorf("invalid directory: %w", err)
		}
		cfg.WorkDir = abs
	} else {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot determine working directory: %w", err)
		}
		cfg.WorkDir = wd
	}

	// Verify work dir exists
	info, err := os.Stat(cfg.WorkDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("working directory does not exist: %s", cfg.WorkDir)
	}

	// Resume flag
	cfg.Resume = *resume

	return cfg, nil
}

// ──────────────────────────────────────────────────────────────
//  Saved config (~/.codebase/config.json)
// ──────────────────────────────────────────────────────────────

type savedConfig struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
}

func configPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codebase", "config.json")
}

func loadSavedConfig() savedConfig {
	path := configPath()
	if path == "" {
		return savedConfig{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return savedConfig{}
	}
	var sc savedConfig
	json.Unmarshal(data, &sc)
	return sc
}

func saveSavedConfig(sc savedConfig) error {
	path := configPath()
	if path == "" {
		return fmt.Errorf("cannot determine home directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func promptForAPIKey() (string, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("  Welcome to Codebase!")
	fmt.Println()
	fmt.Println("  No API key found. You need an OpenAI-compatible API key to use Codebase.")
	fmt.Println("  Get one from: https://platform.openai.com/api-keys")
	fmt.Println()
	fmt.Print("  Enter your API key: ")

	key, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("no API key provided")
	}

	// Ask about base URL for non-OpenAI providers
	fmt.Println()
	fmt.Println("  Using OpenAI by default. If you use a different provider (Groq, local, etc.),")
	fmt.Print("  enter the base URL (or press Enter to skip): ")

	baseInput, _ := reader.ReadString('\n')
	baseURL := strings.TrimSpace(baseInput)

	// Save for next time
	sc := loadSavedConfig()
	sc.APIKey = key
	if baseURL != "" {
		sc.BaseURL = baseURL
	}
	if err := saveSavedConfig(sc); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not save config: %v\n", err)
	} else {
		fmt.Println()
		fmt.Println("  Saved to ~/.codebase/config.json")
	}
	fmt.Println()

	return key, nil
}

// ──────────────────────────────────────────────────────────────
//  Entry point
// ──────────────────────────────────────────────────────────────

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Clean up stale sessions in the background
	go CleanStaleSessions()

	p := tea.NewProgram(
		newAppModel(cfg),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
