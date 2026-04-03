package main

import (
	"fmt"
	"os"
	"strings"
)

// ──────────────────────────────────────────────────────────────
//  Headless mode — run a prompt without TUI
//
//  Usage: codebase run "explain the memory system"
//
//  Outputs everything to stdout: text, tool calls, tool results.
//  Debug info goes to stderr. No BubbleTea, no interactivity.
//  This is for testing and scripting.
// ──────────────────────────────────────────────────────────────

func runHeadless(prompt string) {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Config error: %v\n", err)
		os.Exit(1)
	}

	client := NewLLMClient(cfg.APIKey, cfg.BaseURL, cfg.Model)
	debugLog("headless: model=%s protocol=%s base=%s", client.Model, client.Protocol, client.BaseURL)
	debugLog("headless: prompt=%q", truncateStr(prompt, 120))

	eventCh := make(chan AgentEvent, 64)
	stopCh := make(chan struct{})
	tasks := NewTaskStore()

	agent := NewAgent(client, cfg.WorkDir, eventCh, stopCh, tasks, nil)
	go agent.Run(prompt)

	// Consume events and print them
	for evt := range eventCh {
		switch evt.Type {
		case EventTextDelta:
			fmt.Print(evt.Text)

		case EventToolStart:
			argsPreview := ""
			if evt.Args != nil {
				for k, v := range evt.Args {
					s, ok := v.(string)
					if ok && len(s) > 60 {
						s = s[:57] + "..."
					}
					if ok {
						argsPreview += fmt.Sprintf(" %s=%q", k, s)
					}
				}
			}
			fmt.Fprintf(os.Stderr, "\n[TOOL] %s%s\n", evt.Tool, argsPreview)

		case EventToolResult:
			status := "OK"
			if !evt.Success {
				status = "FAIL"
			}
			// Show truncated output
			output := evt.Output
			lines := strings.Split(output, "\n")
			if len(lines) > 10 {
				output = strings.Join(lines[:10], "\n") + fmt.Sprintf("\n  ... (%d more lines)", len(lines)-10)
			}
			fmt.Fprintf(os.Stderr, "[%s] %s → %d bytes\n", status, evt.Tool, len(evt.Output))
			if !evt.Success || debugMode {
				fmt.Fprintf(os.Stderr, "%s\n", output)
			}

		case EventTurnStart:
			fmt.Fprintf(os.Stderr, "\n--- turn %d ---\n", evt.Turn)

		case EventUsage:
			fmt.Fprintf(os.Stderr, "[USAGE] %d in + %d out tokens\n", evt.Tokens.PromptTokens, evt.Tokens.CompletionTokens)

		case EventError:
			fmt.Fprintf(os.Stderr, "[ERROR] %v\n", evt.Error)

		case EventPermission:
			// Auto-approve in headless mode
			fmt.Fprintf(os.Stderr, "[PERM] auto-approving: %s\n", evt.Permission.Summary)
			if agent.permCh != nil {
				agent.permCh <- PermissionResponse{Allowed: true, TrustLevel: PermTrustAll}
			}

		case EventDone:
			fmt.Fprintf(os.Stderr, "\n[DONE]\n")
			return
		}
	}
}
