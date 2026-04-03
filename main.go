package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	APIKey     string
	BaseURL    string
	Model      string
	WorkDir    string
	Resume     bool // --resume flag: restore previous session
	Debug      bool // --debug flag: verbose logging to stderr
	NeedsSetup bool // true when no API key found — launch setup wizard
}

// debugLog writes to stderr when debug mode is enabled.
// Global so any file can call it without passing config around.
var debugMode bool

func debugLog(format string, args ...any) {
	if debugMode {
		fmt.Fprintf(os.Stderr, "[DEBUG] "+format+"\n", args...)
	}
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func loadConfig() (*Config, error) {
	// CLI flags
	model := flag.String("model", "", "LLM model name (default: MiniMax-M2.7)")
	dir := flag.String("dir", "", "Working directory (default: current dir)")
	baseURL := flag.String("base-url", "", "OpenAI-compatible API base URL")
	resume := flag.Bool("resume", false, "Resume previous session for this directory")
	debug := flag.Bool("debug", false, "Verbose debug output to stderr")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	debugMode = *debug

	if *showVersion {
		fmt.Printf("codebase %s (%s)\n", version, commit)
		os.Exit(0)
	}

	cfg := &Config{}
	saved := loadSavedConfig()

	// API key: env var → saved config → needs setup
	cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	if cfg.APIKey == "" {
		cfg.APIKey = saved.APIKey
	}

	// Base URL
	cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
	if *baseURL != "" {
		cfg.BaseURL = *baseURL
	}
	if cfg.BaseURL == "" {
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
		if saved.Model != "" {
			cfg.Model = saved.Model
		} else {
			cfg.Model = "MiniMax-M2.7"
		}
	}

	// Set glue env vars from saved config so NewGlueClient picks them up
	if saved.GlueAPIKey != "" && os.Getenv("GLUE_API_KEY") == "" {
		os.Setenv("GLUE_API_KEY", saved.GlueAPIKey)
	}
	if saved.GlueBaseURL != "" && os.Getenv("GLUE_BASE_URL") == "" {
		os.Setenv("GLUE_BASE_URL", saved.GlueBaseURL)
	}
	if saved.GlueFastModel != "" && os.Getenv("GLUE_FAST_MODEL") == "" {
		os.Setenv("GLUE_FAST_MODEL", saved.GlueFastModel)
	}
	if saved.GlueSmartModel != "" && os.Getenv("GLUE_SMART_MODEL") == "" {
		os.Setenv("GLUE_SMART_MODEL", saved.GlueSmartModel)
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

	// If no API key found, check if logged in to Codebase — otherwise launch setup wizard
	if cfg.APIKey == "" && !IsLoggedIn() {
		cfg.NeedsSetup = true
	}

	return cfg, nil
}

// ──────────────────────────────────────────────────────────────
//  Saved config (~/.codebase/config.json)
// ──────────────────────────────────────────────────────────────

type savedConfig struct {
	APIKey         string `json:"api_key,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	Model          string `json:"model,omitempty"`
	GlueAPIKey     string `json:"glue_api_key,omitempty"`
	GlueBaseURL    string `json:"glue_base_url,omitempty"`
	GlueFastModel  string `json:"glue_fast_model,omitempty"`
	GlueSmartModel string `json:"glue_smart_model,omitempty"`
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


// ──────────────────────────────────────────────────────────────
//  Entry point
// ──────────────────────────────────────────────────────────────

func main() {
	loadDotEnv()

	// Handle subcommands before flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "login":
			if err := Login(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "logout":
			if err := Logout(); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "pull":
			if len(os.Args) < 3 {
				fmt.Fprintf(os.Stderr, "Usage: codebase pull <project-id>\n")
				os.Exit(1)
			}
			if err := PullProject(os.Args[2]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "whoami":
			creds, err := loadCredentials()
			if err != nil {
				fmt.Println("Not logged in. Run 'codebase login' to authenticate.")
				return
			}
			name := creds.Email
			if name == "" {
				name = creds.UserID
			}
			fmt.Printf("Logged in as: %s\n", name)
			fmt.Printf("Scopes: %s\n", creds.Scopes)
			expired := time.Now().Unix() > creds.ExpiresAt
			if expired {
				fmt.Println("Token: expired (will auto-refresh on next API call)")
			} else {
				fmt.Printf("Token: valid for %d minutes\n", (creds.ExpiresAt-time.Now().Unix())/60)
			}
			return

		case "run":
			// Headless mode — run a prompt without TUI, output everything to stdout.
			// Usage: codebase run "your prompt here"
			// Add -debug for verbose output to stderr.
			debugMode = true // always debug in headless mode
			if len(os.Args) < 3 {
				fmt.Fprintf(os.Stderr, "Usage: codebase run \"your prompt\"\n")
				os.Exit(1)
			}
			prompt := strings.Join(os.Args[2:], " ")
			runHeadless(prompt)
			return
		}
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Terminal safety: restore on any exit path (panic, signal, error)
	defer restoreTerminal()
	defer func() {
		if r := recover(); r != nil {
			restoreTerminal()
			fmt.Fprintf(os.Stderr, "Fatal: %v\n", r)
			os.Exit(1)
		}
	}()

	// Clean up stale sessions in the background
	go CleanStaleSessions()

	app := newAppModel(cfg)
	defer app.Cleanup()

	p := tea.NewProgram(
		app,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// Graceful signal handling — ensure terminal is restored on SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		p.Kill()
	}()

	exitCode := 0
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		exitCode = 1
	}
	// Defers (Cleanup, restoreTerminal) run before os.Exit
	os.Exit(exitCode)
}

// restoreTerminal resets the terminal to a sane state.
// Called on exit, panic, or signal to prevent corruption.
func restoreTerminal() {
	os.Stdout.WriteString("\033[?1049l") // exit alt screen
	os.Stdout.WriteString("\033[?25h")   // show cursor
	os.Stdout.WriteString("\033[?1002l") // disable mouse tracking
	os.Stdout.WriteString("\033[0m")     // reset attributes
}
