package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/codebase-foundation/cli/internal/tool"
)

// ──────────────────────────────────────────────────────────────
//  MCP Manager — connects to external tool servers
//
//  Config in ~/.codebase/config.json:
//    {
//      "mcp_servers": {
//        "github": {
//          "command": "mcp-server-github",
//          "args": ["--token", "$GITHUB_TOKEN"],
//          "transport": "stdio"
//        }
//      }
//    }
// ──────────────────────────────────────────────────────────────

type MCPServerConfig struct {
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	Transport string            `json:"transport"` // "stdio" or "sse"
	URL       string            `json:"url,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

type MCPManager struct {
	mu      sync.RWMutex
	clients map[string]*mcpclient.Client
}

func NewMCPManager() *MCPManager {
	return &MCPManager{clients: make(map[string]*mcpclient.Client)}
}

func (m *MCPManager) LoadAndConnect(registry *tool.Registry) error {
	configs := loadMCPConfig()
	if len(configs) == 0 {
		return nil
	}
	for name, cfg := range configs {
		if err := m.connectServer(name, cfg, registry); err != nil {
			fmt.Fprintf(os.Stderr, "MCP: %s: %v\n", name, err)
		}
	}
	return nil
}

func (m *MCPManager) connectServer(name string, cfg MCPServerConfig, registry *tool.Registry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var c *mcpclient.Client
	var err error

	switch cfg.Transport {
	case "stdio", "":
		command := expandEnvVars(cfg.Command)
		args := make([]string, len(cfg.Args))
		for i, a := range cfg.Args {
			args[i] = expandEnvVars(a)
		}
		var env []string
		if len(cfg.Env) > 0 {
			env = os.Environ()
			for k, v := range cfg.Env {
				env = append(env, k+"="+expandEnvVars(v))
			}
		}
		c, err = mcpclient.NewStdioMCPClient(command, env, args...)
		if err != nil {
			return fmt.Errorf("stdio: %w", err)
		}

	case "sse":
		c, err = mcpclient.NewSSEMCPClient(expandEnvVars(cfg.URL))
		if err != nil {
			return fmt.Errorf("sse: %w", err)
		}

	default:
		return fmt.Errorf("unknown transport %q", cfg.Transport)
	}

	// Initialize
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "codebase-cli",
		Version: version,
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return fmt.Errorf("init: %w", err)
	}

	// Discover tools
	toolsResp, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("list tools: %w", err)
	}

	for _, t := range toolsResp.Tools {
		mcpTool := newMCPTool(name, t, c)
		if !registry.Has(mcpTool.Name()) {
			registry.Register(mcpTool)
		}
	}

	m.mu.Lock()
	m.clients[name] = c
	m.mu.Unlock()

	fmt.Fprintf(os.Stderr, "MCP: %s connected (%d tools)\n", name, len(toolsResp.Tools))
	return nil
}

func (m *MCPManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients = make(map[string]*mcpclient.Client)
}

func (m *MCPManager) ConnectedServers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.clients))
	for n := range m.clients {
		names = append(names, n)
	}
	return names
}

// ──────────────────────────────────────────────────────────────
//  MCPTool — wraps an MCP server tool as our Tool interface
// ──────────────────────────────────────────────────────────────

type MCPTool struct {
	serverName  string
	toolName    string
	fullName    string
	description string
	schema      json.RawMessage
	client      *mcpclient.Client
}

func newMCPTool(serverName string, t mcp.Tool, c *mcpclient.Client) *MCPTool {
	schema, _ := json.Marshal(t.InputSchema)
	return &MCPTool{
		serverName:  serverName,
		toolName:    t.Name,
		fullName:    "mcp__" + serverName + "__" + t.Name,
		description: t.Description,
		schema:      schema,
		client:      c,
	}
}

func (t *MCPTool) Name() string                            { return t.fullName }
func (t *MCPTool) Description() string                     { return t.description }
func (t *MCPTool) Schema() json.RawMessage                 { return t.schema }
func (t *MCPTool) Effects() []tool.Effect                  { return []tool.Effect{tool.EffectNetwork} }
func (t *MCPTool) ConcurrencySafe(_ map[string]any) bool   { return true }

func (t *MCPTool) Execute(ctx context.Context, args map[string]any, _ *tool.Env) tool.Result {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req := mcp.CallToolRequest{}
	req.Params.Name = t.toolName
	req.Params.Arguments = args

	result, err := t.client.CallTool(callCtx, req)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("MCP error (%s/%s): %v", t.serverName, t.toolName, err), Success: false}
	}

	var output strings.Builder
	for _, content := range result.Content {
		switch c := content.(type) {
		case mcp.TextContent:
			output.WriteString(c.Text)
		default:
			data, _ := json.MarshalIndent(content, "", "  ")
			output.Write(data)
		}
	}

	return tool.Result{Output: output.String(), Success: !result.IsError}
}

// ──────────────────────────────────────────────────────────────
//  Config
// ──────────────────────────────────────────────────────────────

func loadMCPConfig() map[string]MCPServerConfig {
	if configs := loadMCPConfigFile(".codebase/config.json"); configs != nil {
		return configs
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return loadMCPConfigFile(filepath.Join(home, ".codebase", "config.json"))
}

func loadMCPConfigFile(path string) map[string]MCPServerConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		MCPServers map[string]MCPServerConfig `json:"mcp_servers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return cfg.MCPServers
}

func expandEnvVars(s string) string {
	return os.ExpandEnv(s)
}
