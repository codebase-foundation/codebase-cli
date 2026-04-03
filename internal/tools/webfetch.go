package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/codebase-foundation/cli/internal/tool"
)

type WebFetch struct{}

func (WebFetch) Name() string                          { return "web_fetch" }
func (WebFetch) ConcurrencySafe(_ map[string]any) bool { return true }
func (WebFetch) Effects() []tool.Effect                { return []tool.Effect{tool.EffectNetwork} }

func (WebFetch) Description() string {
	return "Fetch a URL and return its content. HTML is converted to readable text. " +
		"Use for: reading documentation, API references, issue pages, or any web content. " +
		"Supports HTML, plain text, and JSON responses."
}

func (WebFetch) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"url": {
				"type": "string",
				"description": "The URL to fetch."
			}
		},
		"required": ["url"]
	}`)
}

func (WebFetch) Execute(ctx context.Context, args map[string]any, env *tool.Env) tool.Result {
	rawURL, _ := args["url"].(string)
	if rawURL == "" {
		return tool.Result{Output: "Error: url is required", Success: false}
	}

	// Upgrade http to https
	if strings.HasPrefix(rawURL, "http://") {
		rawURL = "https://" + rawURL[7:]
	}
	if !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, "GET", rawURL, nil)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error: invalid URL: %v", err), Success: false}
	}
	req.Header.Set("User-Agent", "Codebase-CLI/1.0 (coding assistant)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error fetching %s: %v", rawURL, err), Success: false}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return tool.Result{
			Output:  fmt.Sprintf("Error: HTTP %d from %s", resp.StatusCode, rawURL),
			Success: false,
		}
	}

	// Limit to 1MB
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("Error reading response: %v", err), Success: false}
	}

	content := string(body)
	contentType := resp.Header.Get("Content-Type")

	// Convert HTML to readable text
	if strings.Contains(contentType, "text/html") {
		content = htmlToText(content)
	}

	// Use glue to extract relevant content if available
	if env != nil && env.Glue != nil && len(content) > 5000 {
		extracted, err := env.Glue.Classify(ctx, fmt.Sprintf(
			"Extract the main content from this web page. Remove navigation, ads, footers, and boilerplate. Keep code examples, documentation text, and important information. Return only the relevant content:\n\n%s",
			content[:min(len(content), 20000)],
		))
		if err == nil && len(extracted) > 100 {
			content = extracted
		}
	}

	output := TruncateOutput(fmt.Sprintf("URL: %s\n\n%s", rawURL, content), maxOutputChars)
	return tool.Result{Output: output, Success: true}
}

// htmlToText does a basic HTML → text conversion without external deps.
// Strips tags, decodes common entities, collapses whitespace.
func htmlToText(html string) string {
	// Remove script and style blocks
	scriptRe := regexp.MustCompile(`(?is)<(script|style|nav|footer|header)[^>]*>.*?</\1>`)
	text := scriptRe.ReplaceAllString(html, "")

	// Convert common block elements to newlines
	blockRe := regexp.MustCompile(`(?i)</(p|div|li|tr|h[1-6]|br|section|article)>`)
	text = blockRe.ReplaceAllString(text, "\n")
	brRe := regexp.MustCompile(`(?i)<br\s*/?>`)
	text = brRe.ReplaceAllString(text, "\n")

	// Convert list items
	liRe := regexp.MustCompile(`(?i)<li[^>]*>`)
	text = liRe.ReplaceAllString(text, "\n- ")

	// Convert headers
	for i := 1; i <= 6; i++ {
		hRe := regexp.MustCompile(fmt.Sprintf(`(?i)<h%d[^>]*>`, i))
		prefix := strings.Repeat("#", i) + " "
		text = hRe.ReplaceAllString(text, "\n"+prefix)
	}

	// Convert links: <a href="...">text</a> → text (url)
	linkRe := regexp.MustCompile(`(?i)<a[^>]+href="([^"]*)"[^>]*>(.*?)</a>`)
	text = linkRe.ReplaceAllString(text, "$2")

	// Convert code blocks
	codeBlockRe := regexp.MustCompile(`(?is)<pre[^>]*><code[^>]*>(.*?)</code></pre>`)
	text = codeBlockRe.ReplaceAllString(text, "\n```\n$1\n```\n")

	// Strip remaining tags
	tagRe := regexp.MustCompile(`<[^>]+>`)
	text = tagRe.ReplaceAllString(text, "")

	// Decode HTML entities
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&#39;", "'")
	text = strings.ReplaceAll(text, "&nbsp;", " ")

	// Collapse whitespace
	spaceRe := regexp.MustCompile(`[ \t]+`)
	text = spaceRe.ReplaceAllString(text, " ")
	nlRe := regexp.MustCompile(`\n{3,}`)
	text = nlRe.ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}
