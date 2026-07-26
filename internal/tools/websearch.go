package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const webSearchDefaultLimit = 5
const webSearchMaxLimit = 10
const webSearchMaxQueryLen = 500
const webSearchMaxTitleRunes = 300
const webSearchMaxSnippetRunes = 400
const webSearchMaxOutputBytes = 16000

// Snippet text always sits within a few kilobytes of its title anchor; the cap
// keeps a single malformed (unclosed) element from pulling in the whole page.
const webSearchSnippetWindowBytes = 6000

// Layout markers appear in the first screenful of markup, so classification
// never has to scan a full megabyte of body.
const webSearchMarkerScanBytes = 256 << 10

var webSearchEndpoint = "https://duckduckgo.com/html/"

type WebSearchTool struct{}

type webSearchInput struct {
	Query   string `json:"query" desc:"Search terms. The query leaves this machine, so never include private code, paths, hostnames, or secrets."`
	Limit   int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=10" desc:"Maximum number of results to return. Defaults to 5."`
	Timeout int    `json:"timeout,omitempty" jsonschema:"minimum=1" desc:"Request timeout in milliseconds. Defaults to 15000."`
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
	if len(query) > webSearchMaxQueryLen {
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
	// Use the same hardened client as WebFetch. A plain http.Client re-resolves
	// DNS at dial time and follows redirects to any host, so the search endpoint
	// was not covered by the SSRF protections applied to every other outbound
	// request: resolved-IP pinning, private/metadata address rejection, and
	// per-redirect revalidation.
	if _, err := validatePublicFetchURL(fetchCtx, searchURL.String()); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	client := newWebFetchHTTPClient(timeout, net.DefaultResolver, nil)
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
	body := string(data)
	results := parseDuckDuckGoHTMLResults(body, limit)
	status := classifyWebSearchPage(body, len(results))
	meta := map[string]any{
		"query":     query,
		"results":   len(results),
		"source":    "duckduckgo_html",
		"truncated": truncatedBody,
		"parse":     status.String(),
	}
	switch status {
	case webSearchPageParserFailure:
		return Result{Output: fmt.Sprintf("web search parser failure: the backend returned a results page for %q but no results could be extracted. Its HTML markup changed and the WebSearch parser needs updating. This is NOT the same as the search having no matches, so do not conclude that nothing was found.", query), IsError: true, Meta: meta}, nil
	case webSearchPageUnrecognized:
		return Result{Output: fmt.Sprintf("web search failed: the response for %q was not a recognizable results page (most likely a bot challenge, rate-limit, or block page). No results could be extracted.", query), IsError: true, Meta: meta}, nil
	}
	out, _ := truncate(formatWebSearchResults(query, results), webSearchMaxOutputBytes)
	return Result{Output: out, Meta: meta}, nil
}

// webSearchPageStatus separates "the search genuinely matched nothing" from
// "our scraper no longer understands the page". Collapsing the two made every
// parser regression look like a successful empty search to the agent.
type webSearchPageStatus int

const (
	webSearchPageOK webSearchPageStatus = iota
	webSearchPageNoResults
	webSearchPageParserFailure
	webSearchPageUnrecognized
)

func (s webSearchPageStatus) String() string {
	switch s {
	case webSearchPageOK:
		return "ok"
	case webSearchPageNoResults:
		return "no_results"
	case webSearchPageParserFailure:
		return "parser_failure"
	default:
		return "unrecognized_page"
	}
}

var (
	// Attribute values may be double quoted, single quoted, or bare, and may
	// appear in any order, so anchors are matched loosely and then parsed.
	anchorTagRE     = regexp.MustCompile(`(?is)<a\s((?:[^>"']|"[^"]*"|'[^']*')*)>(.*?)</a>`)
	openTagRE       = regexp.MustCompile(`(?is)<(a|div|span|p|td|section|li)\s((?:[^>"']|"[^"]*"|'[^']*')*)>`)
	attrRE          = regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
	classOrIDAttrRE = regexp.MustCompile(`(?is)\b(?:class|id)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	danglingTagRE   = regexp.MustCompile(`(?s)<[^>]*$`)
	noResultsTextRE = regexp.MustCompile(`(?is)>\s*(?:sorry,?\s*)?no\s+results?\b[^<]{0,120}<`)
)

// Class tokens that mark the clickable title link of a result. DuckDuckGo has
// shipped several of these over time and mixes extra layout classes in, so the
// match is on tokens rather than on the whole attribute.
var webSearchTitleClasses = map[string]bool{
	"result__a":            true,
	"result-link":          true,
	"result-title-a":       true,
	"js-result-title-link": true,
}

// Class tokens that mark anchors inside a result that are not its title: the
// snippet body, the displayed green URL, and the pagination button.
var webSearchNonTitleClasses = map[string]bool{
	"result__snippet":     true,
	"result__url":         true,
	"result__extras__url": true,
	"result__check":       true,
	"result__type":        true,
	"result--more__btn":   true,
	"result--more":        true,
}

// Tokens that only appear on a rendered search-results document.
var webSearchResultsPageTokens = map[string]bool{
	"links":          true,
	"links_main":     true,
	"react-results":  true,
	"result":         true,
	"result__body":   true,
	"results":        true,
	"results--main":  true,
	"results_links":  true,
	"serp__results":  true,
	"web-result":     true,
	"organic-result": true,
}

// Tokens DuckDuckGo puts on its "nothing matched" block.
var webSearchNoResultsTokens = map[string]bool{
	"no-results":     true,
	"noresults":      true,
	"msg--noresults": true,
}

type webSearchAnchor struct {
	attrs     map[string]string
	titleHTML string
	tagStart  int
	tagEnd    int
}

func parseDuckDuckGoHTMLResults(body string, limit int) []webSearchResult {
	if limit <= 0 {
		return nil
	}
	candidates := collectWebSearchTitleAnchors(body)
	results := make([]webSearchResult, 0, min(limit, len(candidates)))
	seen := map[string]struct{}{}
	for i, candidate := range candidates {
		href := candidate.attrs["href"]
		if isWebSearchAdHref(href) {
			continue
		}
		target := normalizeSearchResultURL(href)
		title := boundText(cleanSearchText(candidate.titleHTML), webSearchMaxTitleRunes)
		if target == "" || title == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		// A snippet belongs to exactly one result, so the search window stops at
		// the next title anchor even when this result has no snippet at all.
		windowEnd := min(len(body), candidate.tagEnd+webSearchSnippetWindowBytes)
		if i+1 < len(candidates) && candidates[i+1].tagStart < windowEnd {
			windowEnd = candidates[i+1].tagStart
		}
		snippet := extractSearchSnippet(body[candidate.tagEnd:windowEnd])
		results = append(results, webSearchResult{Title: title, URL: target, Snippet: snippet})
		if len(results) >= limit {
			break
		}
	}
	return results
}

// collectWebSearchTitleAnchors returns the anchors that look structurally like
// result titles. Filtering on URL validity happens later so that a rejected
// anchor still terminates the previous result's snippet window.
func collectWebSearchTitleAnchors(body string) []webSearchAnchor {
	matches := anchorTagRE.FindAllStringSubmatchIndex(body, -1)
	anchors := make([]webSearchAnchor, 0, len(matches))
	for _, match := range matches {
		attrs := parseHTMLAttrs(body[match[2]:match[3]])
		classes := strings.Fields(strings.ToLower(attrs["class"]))
		index := make(map[string]bool, len(classes))
		for _, class := range classes {
			index[class] = true
		}
		if hasAnyClass(index, webSearchNonTitleClasses) {
			continue
		}
		if !looksLikeTitleAnchor(attrs, index) {
			continue
		}
		anchors = append(anchors, webSearchAnchor{
			attrs:     attrs,
			titleHTML: body[match[4]:match[5]],
			tagStart:  match[0],
			tagEnd:    match[1],
		})
	}
	return anchors
}

func looksLikeTitleAnchor(attrs map[string]string, classes map[string]bool) bool {
	if hasAnyClass(classes, webSearchTitleClasses) {
		return true
	}
	if strings.Contains(strings.ToLower(attrs["data-testid"]), "result-title") {
		return true
	}
	// The /l/?uddg= wrapper is only ever used for outbound result links, so it
	// identifies a result even if every class name on the page has changed.
	return hasUDDGRedirect(attrs["href"])
}

func hasAnyClass(classes map[string]bool, targets map[string]bool) bool {
	for class := range classes {
		if targets[class] {
			return true
		}
	}
	return false
}

func hasUDDGRedirect(href string) bool {
	parsed, err := url.Parse(strings.TrimSpace(html.UnescapeString(href)))
	if err != nil {
		return false
	}
	return parsed.Query().Get("uddg") != ""
}

// isWebSearchAdHref matches DuckDuckGo's sponsored-link redirector, which
// carries the same result classes as an organic hit but never resolves to the
// advertised page from the HTML alone.
func isWebSearchAdHref(href string) bool {
	lower := strings.ToLower(strings.TrimSpace(html.UnescapeString(href)))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "/y.js") {
		return true
	}
	return strings.Contains(lower, "ad_provider=") || strings.Contains(lower, "ad_domain=")
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

// extractSearchSnippet prefers an element whose class or test id merely
// *contains* "snippet" (rather than equalling one blessed class name) and
// falls back to the densest line of text following the title.
func extractSearchSnippet(window string) string {
	window = danglingTagRE.ReplaceAllString(window, "")
	if snippet := snippetFromMarkedElement(window); snippet != "" {
		return snippet
	}
	return snippetFromPlainText(window)
}

func snippetFromMarkedElement(window string) string {
	for _, match := range openTagRE.FindAllStringSubmatchIndex(window, -1) {
		tag := strings.ToLower(window[match[2]:match[3]])
		attrs := parseHTMLAttrs(window[match[4]:match[5]])
		if !looksLikeSnippetElement(attrs) {
			continue
		}
		inner := innerHTMLOfElement(window, tag, match[1], webSearchSnippetWindowBytes)
		if snippet := boundText(cleanSearchText(inner), webSearchMaxSnippetRunes); snippet != "" {
			return snippet
		}
	}
	return ""
}

func looksLikeSnippetElement(attrs map[string]string) bool {
	if strings.Contains(strings.ToLower(attrs["data-testid"]), "snippet") {
		return true
	}
	for _, class := range strings.Fields(strings.ToLower(attrs["class"])) {
		if strings.Contains(class, "snippet") || strings.Contains(class, "abstract") {
			return true
		}
	}
	return false
}

func snippetFromPlainText(window string) string {
	var longest string
	for _, line := range strings.Split(htmlToText(window), "\n") {
		line = sanitizeSearchText(line)
		if line == "" || looksLikeBareURL(line) {
			continue
		}
		if len(line) > len(longest) {
			longest = line
		}
		if len(line) >= 60 {
			return boundText(line, webSearchMaxSnippetRunes)
		}
	}
	return boundText(longest, webSearchMaxSnippetRunes)
}

func looksLikeBareURL(line string) bool {
	if strings.ContainsAny(line, " \t") {
		return false
	}
	return strings.Contains(line, "://") || strings.Contains(line, ".com/") || strings.Contains(line, ".org/")
}

// innerHTMLOfElement returns the content of an element of tagName whose opening
// tag ends at contentStart, tracking nesting depth so that a snippet wrapping a
// same-named child element is not truncated at the child's closing tag. Go's
// regexp engine has no backreferences, so the match has to be found by scanning.
func innerHTMLOfElement(body, tagName string, contentStart, maxBytes int) string {
	if contentStart >= len(body) {
		return ""
	}
	window := body[contentStart:min(len(body), contentStart+maxBytes)]
	lower := strings.ToLower(window)
	open, closing := "<"+tagName, "</"+tagName
	depth := 0
	for cursor := 0; cursor < len(lower); {
		nextClose := indexTagFrom(lower, closing, cursor)
		if nextClose < 0 {
			return window
		}
		nextOpen := indexTagFrom(lower, open, cursor)
		if nextOpen >= 0 && nextOpen < nextClose {
			depth++
			cursor = nextOpen + len(open)
			continue
		}
		if depth == 0 {
			return window[:nextClose]
		}
		depth--
		cursor = nextClose + len(closing)
	}
	return window
}

// indexTagFrom finds prefix at a real tag boundary, so that looking for "<p"
// does not match "<pre" and "</a" does not match "</abbr".
func indexTagFrom(lower, prefix string, from int) int {
	for from < len(lower) {
		at := strings.Index(lower[from:], prefix)
		if at < 0 {
			return -1
		}
		at += from
		after := at + len(prefix)
		if after >= len(lower) {
			return -1
		}
		switch lower[after] {
		case ' ', '\t', '\n', '\r', '\f', '>', '/':
			return at
		}
		from = after
	}
	return -1
}

func classifyWebSearchPage(body string, parsed int) webSearchPageStatus {
	if parsed > 0 {
		return webSearchPageOK
	}
	scan := body[:min(len(body), webSearchMarkerScanBytes)]
	// Inline scripts carry class names and UI strings for states the page is not
	// in, so they are removed before any marker is trusted.
	scan = scriptRE.ReplaceAllString(scan, "")
	scan = styleRE.ReplaceAllString(scan, "")
	scan = noscriptRE.ReplaceAllString(scan, "")
	// Outbound /l/?uddg= links exist only for actual hits, so finding them while
	// having extracted nothing outranks any no-results marker on the page.
	if strings.Contains(scan, "uddg=") {
		return webSearchPageParserFailure
	}
	tokens := collectClassAndIDTokens(scan)
	if hasAnyClass(tokens, webSearchNoResultsTokens) || noResultsTextRE.MatchString(scan) {
		return webSearchPageNoResults
	}
	if hasAnyClass(tokens, webSearchResultsPageTokens) {
		return webSearchPageParserFailure
	}
	return webSearchPageUnrecognized
}

func collectClassAndIDTokens(body string) map[string]bool {
	tokens := map[string]bool{}
	for _, match := range classOrIDAttrRE.FindAllStringSubmatch(body, -1) {
		value := match[1]
		if value == "" {
			value = match[2]
		}
		if value == "" {
			value = match[3]
		}
		for _, token := range strings.Fields(strings.ToLower(html.UnescapeString(value))) {
			tokens[token] = true
		}
	}
	return tokens
}

func cleanSearchText(raw string) string {
	return sanitizeSearchText(htmlToText(raw))
}

// sanitizeSearchText drops control and formatting runes before remote text
// reaches the transcript: a hostile page could otherwise smuggle ANSI escapes
// or bidi overrides into a title and rewrite how surrounding output reads.
func sanitizeSearchText(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, raw)
	return strings.Join(strings.Fields(cleaned), " ")
}

func boundText(s string, maxRunes int) string {
	if maxRunes <= 0 || s == "" {
		return ""
	}
	if len(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return strings.TrimRight(string(runes[:maxRunes]), " ") + "..."
}

// normalizeSearchResultURL unwraps DuckDuckGo's /l/?uddg= redirect and returns
// "" for anything that is not a public http(s) destination, so relative
// navigation links and javascript: payloads never surface as results.
func normalizeSearchResultURL(raw string) string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	// url.Values already percent-decodes; decoding a second time would corrupt
	// destinations that legitimately contain an encoded percent sign.
	if encoded := strings.TrimSpace(parsed.Query().Get("uddg")); encoded != "" {
		inner, innerErr := url.Parse(encoded)
		if innerErr != nil || !isPublicWebURL(inner) {
			return ""
		}
		return inner.String()
	}
	if isPublicWebURL(parsed) {
		return parsed.String()
	}
	if strings.HasPrefix(raw, "//") {
		if promoted, promoteErr := url.Parse("https:" + raw); promoteErr == nil && isPublicWebURL(promoted) {
			return promoted.String()
		}
	}
	return ""
}

func isPublicWebURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.Host != ""
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
