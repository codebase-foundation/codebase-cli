package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────
//  Web Search — multi-provider search for the agent
//
//  Env vars:
//    SEARCH_PROVIDER  — "tavily", "brave", or "duckduckgo" (default)
//    SEARCH_API_KEY   — required for tavily/brave
//    SEARXNG_URL      — base URL for self-hosted SearXNG instance
//
//  The agent calls web_search as a tool. Results are returned
//  as formatted text: title + URL + snippet for each result.
// ──────────────────────────────────────────────────────────────

// SearchResult is a single web search result.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchResponse is the full response from a search query.
type SearchResponse struct {
	Query   string         `json:"query"`
	Answer  string         `json:"answer,omitempty"` // Tavily can provide a direct answer
	Results []SearchResult `json:"results"`
}

// WebSearch executes a web search using the configured provider.
func WebSearch(query string, maxResults int) (*SearchResponse, error) {
	if query == "" {
		return nil, fmt.Errorf("empty search query")
	}
	if maxResults <= 0 {
		maxResults = 5
	}
	if maxResults > 10 {
		maxResults = 10
	}

	provider := strings.ToLower(os.Getenv("SEARCH_PROVIDER"))
	switch provider {
	case "tavily":
		return searchTavily(query, maxResults)
	case "brave":
		return searchBrave(query, maxResults)
	case "searxng":
		return searchSearXNG(query, maxResults)
	default:
		// Auto-detect from available keys
		if os.Getenv("TAVILY_API_KEY") != "" || os.Getenv("SEARCH_API_KEY") != "" {
			return searchTavily(query, maxResults)
		}
		if os.Getenv("BRAVE_API_KEY") != "" {
			return searchBrave(query, maxResults)
		}
		if os.Getenv("SEARXNG_URL") != "" {
			return searchSearXNG(query, maxResults)
		}
		return searchDuckDuckGo(query, maxResults)
	}
}

// FormatSearchResults converts search results to agent-readable text.
func FormatSearchResults(resp *SearchResponse) string {
	var sb strings.Builder

	if resp.Answer != "" {
		sb.WriteString("Direct answer: " + resp.Answer + "\n\n")
	}

	sb.WriteString(fmt.Sprintf("Found %d results for %q:\n\n", len(resp.Results), resp.Query))

	for i, r := range resp.Results {
		sb.WriteString(fmt.Sprintf("[%d] %s\n", i+1, r.Title))
		sb.WriteString(fmt.Sprintf("    %s\n", r.URL))
		if r.Snippet != "" {
			// Truncate long snippets
			snippet := r.Snippet
			if len(snippet) > 300 {
				snippet = snippet[:297] + "..."
			}
			sb.WriteString(fmt.Sprintf("    %s\n", snippet))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// ──────────────────────────────────────────────────────────────
//  Tavily (AI-optimized search)
// ──────────────────────────────────────────────────────────────

func searchTavily(query string, maxResults int) (*SearchResponse, error) {
	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("SEARCH_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("TAVILY_API_KEY or SEARCH_API_KEY required for Tavily search")
	}

	body := map[string]interface{}{
		"query":          query,
		"max_results":    maxResults,
		"search_depth":   "basic",
		"include_answer": true,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.tavily.com/search", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Tavily API %d: %s", resp.StatusCode, string(errBody))
	}

	var tavilyResp struct {
		Query   string `json:"query"`
		Answer  string `json:"answer"`
		Results []struct {
			Title   string  `json:"title"`
			URL     string  `json:"url"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tavilyResp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	results := make([]SearchResult, len(tavilyResp.Results))
	for i, r := range tavilyResp.Results {
		results[i] = SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		}
	}

	return &SearchResponse{
		Query:   query,
		Answer:  tavilyResp.Answer,
		Results: results,
	}, nil
}

// ──────────────────────────────────────────────────────────────
//  Brave Search
// ──────────────────────────────────────────────────────────────

func searchBrave(query string, maxResults int) (*SearchResponse, error) {
	apiKey := os.Getenv("BRAVE_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("SEARCH_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("BRAVE_API_KEY or SEARCH_API_KEY required for Brave search")
	}

	u := fmt.Sprintf("https://api.search.brave.com/res/v1/web/search?q=%s&count=%d",
		url.QueryEscape(query), maxResults)

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", apiKey)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Brave API %d: %s", resp.StatusCode, string(errBody))
	}

	var braveResp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&braveResp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	results := make([]SearchResult, len(braveResp.Web.Results))
	for i, r := range braveResp.Web.Results {
		results[i] = SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Description,
		}
	}

	return &SearchResponse{
		Query:   query,
		Results: results,
	}, nil
}

// ──────────────────────────────────────────────────────────────
//  SearXNG (self-hosted)
// ──────────────────────────────────────────────────────────────

func searchSearXNG(query string, maxResults int) (*SearchResponse, error) {
	baseURL := os.Getenv("SEARXNG_URL")
	if baseURL == "" {
		return nil, fmt.Errorf("SEARXNG_URL required for SearXNG search")
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	u := fmt.Sprintf("%s/search?q=%s&format=json&pageno=1",
		baseURL, url.QueryEscape(query))

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("SearXNG %d: %s", resp.StatusCode, string(errBody))
	}

	var searxResp struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&searxResp); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	results := make([]SearchResult, 0, maxResults)
	for i, r := range searxResp.Results {
		if i >= maxResults {
			break
		}
		results = append(results, SearchResult{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		})
	}

	return &SearchResponse{
		Query:   query,
		Results: results,
	}, nil
}

// ──────────────────────────────────────────────────────────────
//  DuckDuckGo (zero-config fallback)
//  Uses the HTML endpoint + lite parsing. No API key needed.
// ──────────────────────────────────────────────────────────────

func searchDuckDuckGo(query string, maxResults int) (*SearchResponse, error) {
	// Use the DDG lite HTML endpoint
	u := "https://lite.duckduckgo.com/lite/"

	form := url.Values{}
	form.Set("q", query)

	req, err := http.NewRequest("POST", u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Codebase-CLI/1.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DuckDuckGo %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	html := string(bodyBytes)

	results := parseDDGLite(html, maxResults)

	return &SearchResponse{
		Query:   query,
		Results: results,
	}, nil
}

// parseDDGLite extracts search results from DuckDuckGo Lite HTML.
// The lite page has a table with class="result-link" anchors and
// class="result-snippet" text nodes.
// findClass locates a class attribute in HTML, handling both single and double quotes.
// Returns the index into s where the match starts, or -1.
func findClass(s, className string) int {
	// Try double quotes first, then single quotes
	patterns := []string{
		`class="` + className + `"`,
		`class='` + className + `'`,
	}
	best := -1
	for _, p := range patterns {
		idx := strings.Index(s, p)
		if idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	return best
}

// extractAttr extracts an HTML attribute value, handling both quote styles.
// Returns (value, rest-of-string-after-closing-quote) or ("", s) on failure.
func extractAttr(s, attr string) (string, string) {
	for _, q := range []string{`"`, `'`} {
		needle := attr + `=` + q
		idx := strings.Index(s, needle)
		if idx < 0 {
			continue
		}
		after := s[idx+len(needle):]
		end := strings.Index(after, q)
		if end < 0 {
			continue
		}
		return after[:end], after[end+1:]
	}
	return "", s
}

func parseDDGLite(html string, maxResults int) []SearchResult {
	var results []SearchResult

	remaining := html
	for len(results) < maxResults {
		// Find next result link
		linkIdx := findClass(remaining, "result-link")
		if linkIdx < 0 {
			break
		}
		remaining = remaining[linkIdx:]

		// Extract href
		href, rest := extractAttr(remaining, "href")
		if href == "" {
			// Skip past this match to avoid infinite loop
			remaining = remaining[len("result-link"):]
			continue
		}
		remaining = rest

		// Extract title (text between > and </a>)
		tagClose := strings.Index(remaining, ">")
		if tagClose < 0 {
			break
		}
		remaining = remaining[tagClose+1:]
		aEnd := strings.Index(remaining, "</a>")
		if aEnd < 0 {
			break
		}
		title := stripHTML(remaining[:aEnd])
		remaining = remaining[aEnd:]

		// Extract snippet: look for result-snippet nearby
		snippet := ""
		window := remaining
		if len(window) > 2000 {
			window = window[:2000]
		}
		snippetIdx := findClass(window, "result-snippet")
		if snippetIdx >= 0 {
			snippetHTML := remaining[snippetIdx:]
			tagEnd := strings.Index(snippetHTML, ">")
			if tagEnd >= 0 {
				snippetHTML = snippetHTML[tagEnd+1:]
				closeTag := strings.Index(snippetHTML, "</td>")
				if closeTag < 0 {
					closeTag = strings.Index(snippetHTML, "</span>")
				}
				if closeTag > 0 {
					snippet = stripHTML(snippetHTML[:closeTag])
				}
			}
		}

		if href != "" && title != "" {
			results = append(results, SearchResult{
				Title:   strings.TrimSpace(title),
				URL:     strings.TrimSpace(href),
				Snippet: strings.TrimSpace(snippet),
			})
		}
	}

	return results
}

// stripHTML removes HTML tags from a string.
func stripHTML(s string) string {
	var result strings.Builder
	inTag := false
	for _, c := range s {
		if c == '<' {
			inTag = true
			continue
		}
		if c == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteRune(c)
		}
	}
	return strings.TrimSpace(result.String())
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
