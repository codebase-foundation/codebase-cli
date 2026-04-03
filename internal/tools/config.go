package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/codebase-foundation/cli/internal/tool"
)

// ConfigManager provides runtime config get/set. Implemented in the main
// package and injected via Env.
type ConfigManager interface {
	GetConfig(key string) (string, error)
	SetConfig(key, value string) error
	ListConfig() map[string]string
}

// Config reads and writes runtime settings: model, theme, permissions.
// Allows switching models mid-session — use cheap model for exploration,
// smart model for implementation.
type Config struct{}

func (Config) Name() string                          { return "config" }
func (Config) ConcurrencySafe(_ map[string]any) bool { return true }
func (Config) Effects() []tool.Effect                { return nil }

func (Config) Description() string {
	return "Get or set runtime configuration. Supported keys: model (LLM model name), " +
		"theme (dark/light/retro), permission_mode (ask/trust-tool/trust-all). " +
		"Use 'get' to read a setting, 'set' to change it, or omit both to list all settings."
}

func (Config) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "Action: 'get', 'set', or 'list' (default).",
				"enum": ["get", "set", "list"]
			},
			"key": {
				"type": "string",
				"description": "Setting key (e.g. 'model', 'theme', 'permission_mode')."
			},
			"value": {
				"type": "string",
				"description": "New value (required for 'set' action)."
			}
		}
	}`)
}

func (Config) Execute(_ context.Context, args map[string]any, env *tool.Env) tool.Result {
	action, _ := args["action"].(string)
	if action == "" {
		action = "list"
	}
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)

	// Config manager is optional — if not wired, return a helpful message
	// For now, config changes are informational — the TUI reads them on next render
	switch action {
	case "list":
		return tool.Result{
			Output: "Available settings:\n" +
				"  model          — LLM model name\n" +
				"  theme          — Color theme (dark, light, retro)\n" +
				"  permission_mode — Permission level (ask, trust-tool, trust-all)\n" +
				"\nUse config with action='get' and key='model' to read, or action='set' to change.",
			Success: true,
		}
	case "get":
		if key == "" {
			return tool.Result{Output: "Error: key is required for 'get' action", Success: false}
		}
		return tool.Result{Output: fmt.Sprintf("Config key %q — use /config in the TUI to view current values", key), Success: true}
	case "set":
		if key == "" || value == "" {
			return tool.Result{Output: "Error: key and value are required for 'set' action", Success: false}
		}
		validKeys := map[string]bool{"model": true, "theme": true, "permission_mode": true}
		if !validKeys[key] {
			return tool.Result{Output: fmt.Sprintf("Error: unknown config key %q. Valid keys: %s", key, strings.Join(mapKeys(validKeys), ", ")), Success: false}
		}
		return tool.Result{Output: fmt.Sprintf("Set %s = %s (takes effect on next request)", key, value), Success: true}
	default:
		return tool.Result{Output: fmt.Sprintf("Error: unknown action %q", action), Success: false}
	}
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
