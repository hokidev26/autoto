package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const webSearchDefaultLimit = 5
const webSearchMaxLimit = 10

var webSearchEndpoint = "https://duckduckgo.com/html/"

type WebSearchTool struct{}

type webSearchInput struct {
	Query   string `json:"query"`
	Limit   int    `json:"limit,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

type webSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

func (WebSearchTool) Name() string { return "WebSearch" }
func (WebSearchTool) Description() string {
	return "Search the public web and return concise result titles, URLs, and snippets for documentation lookup."
}
func (WebSearchTool) Schema() any               { return webSearchInput{} }
func (WebSearchTool) Risk(json.RawMessage) Risk { return RiskRead }

func (WebSearchTool) Execute(ctx context.Context, call Call, _ Env) (Result, error) {
	var input webSearchInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return Result{Output: "query is required", IsError: true}, nil
	}
	if len(query) > 500 {
		return Result{Output: "query is too long", IsError: true}, nil
	}
	limit := input.Limit
	if limit <= 0 {
		limit = webSearchDefaultLimit
	}
	if limit > webSearchMaxLimit {
		limit = webSearchMaxLimit
	}
	timeout := time.Duration(input.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	endpoint, err := validatePublicFetchURL(ctx, webSearchEndpoint)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	searchURL := *endpoint
	values := searchURL.Query()
	values.Set("q", query)
	searchURL.RawQuery = values.Encode()

	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "Autoto-WebSearch/0.1")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.1")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{Output: fmt.Sprintf("search failed with status %s", resp.Status), IsError: true, Meta: map[string]any{"status": resp.StatusCode, "query": query}}, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes+1))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	truncatedBody := len(data) > webFetchMaxBytes
	if truncatedBody {
		data = data[:webFetchMaxBytes]
	}
	results := parseDuckDuckGoHTMLResults(string(data), limit)
	return Result{Output: formatWebSearchResults(query, results), Meta: map[string]any{"query": query, "results": len(results), "source": "duckduckgo_html", "truncated": truncatedBody}}, nil
}

var anchorTagRE = regexp.MustCompile(`(?is)<a\s+([^>]*)>(.*?)</a>`)
var attrRE = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
var snippetElementRE = regexp.MustCompile(`(?is)<(?:a|div|span)[^>]+class=["'][^"']*result__snippet[^"']*["'][^>]*>(.*?)</(?:a|div|span)>`)

func parseDuckDuckGoHTMLResults(body string, limit int) []webSearchResult {
	if limit <= 0 {
		return nil
	}
	matches := anchorTagRE.FindAllStringSubmatchIndex(body, -1)
	results := make([]webSearchResult, 0, min(limit, len(matches)))
	seen := map[string]struct{}{}
	for _, match := range matches {
		attrs := parseHTMLAttrs(body[match[2]:match[3]])
		classes := strings.Fields(attrs["class"])
		if !containsClass(classes, "result__a") {
			continue
		}
		href := normalizeSearchResultURL(attrs["href"])
		title := cleanSearchText(body[match[4]:match[5]])
		if href == "" || title == "" {
			continue
		}
		if _, ok := seen[href]; ok {
			continue
		}
		seen[href] = struct{}{}
		windowEnd := min(len(body), match[1]+4000)
		snippet := firstSearchSnippet(body[match[1]:windowEnd])
		results = append(results, webSearchResult{Title: title, URL: href, Snippet: snippet})
		if len(results) >= limit {
			break
		}
	}
	return results
}

func parseHTMLAttrs(attrs string) map[string]string {
	out := map[string]string{}
	for _, match := range attrRE.FindAllStringSubmatch(attrs, -1) {
		if len(match) < 6 {
			continue
		}
		value := match[3]
		if value == "" {
			value = match[4]
		}
		if value == "" {
			value = match[5]
		}
		out[strings.ToLower(match[1])] = html.UnescapeString(value)
	}
	return out
}

func containsClass(classes []string, target string) bool {
	for _, class := range classes {
		if class == target {
			return true
		}
	}
	return false
}

func firstSearchSnippet(body string) string {
	match := snippetElementRE.FindStringSubmatch(body)
	if len(match) < 2 {
		return ""
	}
	return cleanSearchText(match[1])
}

func cleanSearchText(raw string) string {
	text := htmlToText(raw)
	return strings.Join(strings.Fields(text), " ")
}

func normalizeSearchResultURL(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err == nil {
		if encoded := parsed.Query().Get("uddg"); encoded != "" {
			if decoded, decodeErr := url.QueryUnescape(encoded); decodeErr == nil && strings.TrimSpace(decoded) != "" {
				return strings.TrimSpace(decoded)
			}
		}
		if parsed.Scheme == "http" || parsed.Scheme == "https" {
			return parsed.String()
		}
	}
	return raw
}

func formatWebSearchResults(query string, results []webSearchResult) string {
	if len(results) == 0 {
		return "No search results found for " + query
	}
	var builder strings.Builder
	builder.WriteString("Search results for ")
	builder.WriteString(query)
	builder.WriteString(":\n")
	for i, result := range results {
		builder.WriteString(fmt.Sprintf("\n%d. %s\n   URL: %s", i+1, result.Title, result.URL))
		if result.Snippet != "" {
			builder.WriteString("\n   Snippet: ")
			builder.WriteString(result.Snippet)
		}
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}
