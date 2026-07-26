package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A realistic DuckDuckGo /html/ results page. It deliberately mixes attribute
// orders, single and double quotes, extra layout classes, entity-encoded text,
// the /l/?uddg= redirect wrapper, a sponsored /y.js link, a duplicate URL, a
// javascript: payload smuggled through uddg, and a result block whose class
// names are unknown to the parser.
const ddgResultsPageHTML = `<!DOCTYPE html>
<html lang="en">
<head><title>go generics at DuckDuckGo</title></head>
<body class="body--html">
<form id="search_form" action="/html/"><input name="q" value="go generics"></form>
<div id="links" class="results">

  <div class="result results_links results_links_deep web-result">
    <div class="links_main links_deep result__body">
      <h2 class="result__title">
        <a rel="nofollow" class="result__a extra-class" href="/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Ftutorial%2Fgenerics&amp;rut=9f">Tutorial: Generics &amp; Type&nbsp;Parameters</a>
      </h2>
      <a class="result__snippet" href="/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Ftutorial%2Fgenerics">This tutorial introduces <b>generics</b> in <span>Go</span>, including type parameters.</a>
      <div class="result__extras">
        <a class="result__url" href="/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Ftutorial%2Fgenerics">go.dev/doc/tutorial/generics</a>
      </div>
    </div>
  </div>

  <div class='result'>
    <a href='https://pkg.go.dev/cmp' data-testid='result-title-a' class='js-result-title-link'>pkg.go.dev &mdash; cmp</a>
    <div class='snippet-text'>Package cmp provides <div class="hl">ordered</div> comparisons.</div>
  </div>

  <div class="result result--ad">
    <a class="result__a" href="/y.js?ad_provider=bingv7aa&amp;u3=https%3A%2F%2Fads.example.com">Sponsored Go Course</a>
  </div>

  <div class="result">
    <a class="result__a" href="/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2Ftutorial%2Fgenerics">Duplicate Of First</a>
  </div>

  <div class="result">
    <a class="result__a" href="/l/?uddg=javascript%3Aalert%281%29">Hostile Link</a>
  </div>

  <div class="serp-item">
    <h2><a href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgobyexample.com%2Fgenerics">Go by Example: Generics</a></h2>
    <p class="result-snippet-body">Generics <em>by</em> example.</p>
  </div>

  <a class="result--more__btn" href="/html/?q=go+generics&amp;s=30">More results</a>
</div>
</body></html>`

// DuckDuckGo's genuine "nothing matched" page: still a results document, but
// with the distinctive no-results block in place of any result.
const ddgNoResultsPageHTML = `<!DOCTYPE html>
<html lang="en">
<head><title>zzqqxxwwvv at DuckDuckGo</title></head>
<body class="body--html">
<form id="search_form" action="/html/"><input name="q" value="zzqqxxwwvv"></form>
<div id="links" class="results">
  <div class="no-results">No results.</div>
</div>
</body></html>`

// A results document whose result markup moved into a client-rendered payload.
// The JSON blob mentions "no results" so that stripping <script> before looking
// for the no-results marker is actually exercised.
const ddgShapeChangedPageHTML = `<!DOCTYPE html>
<html lang="en">
<head><title>go generics at DuckDuckGo</title></head>
<body class="body--html">
<div id="links" class="results js-results"></div>
<script type="application/json" id="ddg-state">{"hits":[{"t":"Tutorial: Generics","u":"https://go.dev/doc/tutorial/generics"}],"emptyLabel":"no results found"}</script>
</body></html>`

// A bot-challenge interstitial: not a results document at all.
const ddgChallengePageHTML = `<!DOCTYPE html>
<html lang="en">
<head><title>DuckDuckGo</title></head>
<body class="body--anomaly">
<div class="anomaly-modal__mask">
  <div class="anomaly-modal__title">Unfortunately, bots use DuckDuckGo too.</div>
  <p>Please complete the challenge below to continue.</p>
</div>
</body></html>`

func TestParseDuckDuckGoHTMLResultsRealisticPage(t *testing.T) {
	results := parseDuckDuckGoHTMLResults(ddgResultsPageHTML, 10)
	want := []webSearchResult{
		{
			Title:   "Tutorial: Generics & Type Parameters",
			URL:     "https://go.dev/doc/tutorial/generics",
			Snippet: "This tutorial introduces generics in Go, including type parameters.",
		},
		{
			Title:   "pkg.go.dev — cmp",
			URL:     "https://pkg.go.dev/cmp",
			Snippet: "Package cmp provides ordered comparisons.",
		},
		{
			Title:   "Go by Example: Generics",
			URL:     "https://gobyexample.com/generics",
			Snippet: "Generics by example.",
		},
	}
	if len(results) != len(want) {
		t.Fatalf("expected %d results, got %d: %+v", len(want), len(results), results)
	}
	for i := range want {
		if results[i] != want[i] {
			t.Errorf("result %d:\n got %+v\nwant %+v", i, results[i], want[i])
		}
	}
	for _, result := range results {
		if strings.Contains(result.Title, "Sponsored") || strings.Contains(result.URL, "y.js") {
			t.Errorf("sponsored link leaked into results: %+v", result)
		}
	}
}

func TestParseDuckDuckGoHTMLResultsRespectsLimit(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "zero returns nothing", limit: 0, want: 0},
		{name: "negative returns nothing", limit: -3, want: 0},
		{name: "one", limit: 1, want: 1},
		{name: "two", limit: 2, want: 2},
		{name: "more than available", limit: 50, want: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDuckDuckGoHTMLResults(ddgResultsPageHTML, tc.limit)
			if len(got) != tc.want {
				t.Fatalf("limit %d: expected %d results, got %d: %+v", tc.limit, tc.want, len(got), got)
			}
		})
	}
}

func TestParseDuckDuckGoHTMLResultsNonResultPages(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "no results page", body: ddgNoResultsPageHTML},
		{name: "shape changed page", body: ddgShapeChangedPageHTML},
		{name: "challenge page", body: ddgChallengePageHTML},
		{name: "empty body", body: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDuckDuckGoHTMLResults(tc.body, 10); len(got) != 0 {
				t.Fatalf("expected no results, got %+v", got)
			}
		})
	}
}

func TestClassifyWebSearchPage(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		parsed int
		want   webSearchPageStatus
	}{
		{name: "results parsed", body: ddgResultsPageHTML, parsed: 3, want: webSearchPageOK},
		{name: "genuine empty search", body: ddgNoResultsPageHTML, want: webSearchPageNoResults},
		{name: "markup changed", body: ddgShapeChangedPageHTML, want: webSearchPageParserFailure},
		{name: "bot challenge", body: ddgChallengePageHTML, want: webSearchPageUnrecognized},
		{name: "results page parsed nothing", body: ddgResultsPageHTML, want: webSearchPageParserFailure},
		{name: "empty body", body: "", want: webSearchPageUnrecognized},
		{
			name: "no results phrased as sentence",
			body: `<html><body><div id="serp"><p>Sorry, no results found for that query.</p></div></body></html>`,
			want: webSearchPageNoResults,
		},
		{
			name: "unknown container with redirect wrapper is a parser failure",
			body: `<html><body><section class="brand-new"><span data-href="/l/?uddg=https%3A%2F%2Fexample.com"></span></section></body></html>`,
			want: webSearchPageParserFailure,
		},
		{
			name: "no-results marker alongside outbound hits is a parser failure",
			body: `<html><body><div class="no-results">No results.</div><div class="x"><span data-url="/l/?uddg=https%3A%2F%2Fexample.com">hit</span></div></body></html>`,
			want: webSearchPageParserFailure,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyWebSearchPage(tc.body, tc.parsed)
			if got != tc.want {
				t.Fatalf("classify = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestWebSearchPageStatusString(t *testing.T) {
	cases := map[webSearchPageStatus]string{
		webSearchPageOK:            "ok",
		webSearchPageNoResults:     "no_results",
		webSearchPageParserFailure: "parser_failure",
		webSearchPageUnrecognized:  "unrecognized_page",
	}
	for status, want := range cases {
		if got := status.String(); got != want {
			t.Errorf("status %d = %q, want %q", int(status), got, want)
		}
	}
}

func TestNormalizeSearchResultURL(t *testing.T) {
	cases := []struct {
		name string
		href string
		want string
	}{
		{name: "uddg redirect", href: "/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F", want: "https://go.dev/doc/"},
		{name: "uddg with extra params", href: "/l/?uddg=https%3A%2F%2Fgo.dev%2F&rut=abc", want: "https://go.dev/"},
		{name: "protocol relative redirect", href: "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa%3Fb%3D1", want: "https://example.com/a?b=1"},
		{name: "entity encoded ampersand", href: "/l/?uddg=https%3A%2F%2Fexample.com%2Fa&amp;rut=1", want: "https://example.com/a"},
		{name: "single percent decode only", href: "/l/?uddg=https%3A%2F%2Fexample.com%2Fp%2520q", want: "https://example.com/p%20q"},
		{name: "javascript payload rejected", href: "/l/?uddg=javascript%3Aalert%281%29", want: ""},
		{name: "data payload rejected", href: "/l/?uddg=data%3Atext%2Fhtml%2Chi", want: ""},
		{name: "empty uddg rejected", href: "/l/?uddg=", want: ""},
		{name: "relative navigation rejected", href: "/settings?ko=-1", want: ""},
		{name: "fragment rejected", href: "#top", want: ""},
		{name: "ftp rejected", href: "ftp://example.com/x", want: ""},
		{name: "plain https", href: "https://example.com/x", want: "https://example.com/x"},
		{name: "surrounding whitespace", href: "  https://example.com/y  ", want: "https://example.com/y"},
		{name: "protocol relative plain", href: "//example.com/plain", want: "https://example.com/plain"},
		{name: "malformed", href: "http://[::1", want: ""},
		{name: "empty", href: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeSearchResultURL(tc.href); got != tc.want {
				t.Fatalf("normalizeSearchResultURL(%q) = %q, want %q", tc.href, got, tc.want)
			}
		})
	}
}

func TestExtractSearchSnippet(t *testing.T) {
	cases := []struct {
		name   string
		window string
		want   string
	}{
		{
			name:   "classic class name",
			window: `<div class="result__snippet">Hello <b>there</b>.</div>`,
			want:   "Hello there.",
		},
		{
			name:   "unknown class containing snippet",
			window: `<span class="x-web-snippet--body">Renamed but still a snippet.</span>`,
			want:   "Renamed but still a snippet.",
		},
		{
			name:   "abstract class",
			window: `<div class='search-result-abstract'>Abstract text.</div>`,
			want:   "Abstract text.",
		},
		{
			name:   "data-testid marker",
			window: `<div data-testid="result-snippet" class="opaque">Testid text.</div>`,
			want:   "Testid text.",
		},
		{
			name:   "nested same tag is not truncated",
			window: `<div class="snippet">Outer <div class="hl">inner</div> tail.</div><div class="other">next</div>`,
			want:   "Outer inner tail.",
		},
		{
			name:   "similar tag prefix is not a boundary",
			window: `<p class="snippet">Before <pre>code</pre> after.</p>`,
			want:   "Before code after.",
		},
		{
			name:   "entities decoded",
			window: `<div class="result__snippet">Tom &amp; Jerry &lt;3</div>`,
			want:   "Tom & Jerry <3",
		},
		{
			name:   "falls back to text when no marked element",
			window: `<div class="result__body">A reasonably long description sentence with no snippet class at all.</div>`,
			want:   "A reasonably long description sentence with no snippet class at all.",
		},
		{
			name:   "fallback skips bare url line",
			window: `<div class="url">https://example.com/some/page</div><div class="body">The actual descriptive text that a reader wants to see here.</div>`,
			want:   "The actual descriptive text that a reader wants to see here.",
		},
		{
			name:   "no text at all",
			window: `<div class="empty"></div>`,
			want:   "",
		},
		{
			name:   "dangling tag at window edge is dropped",
			window: `<div class="result__snippet">Cut here.</div><div class="next" data-x="`,
			want:   "Cut here.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractSearchSnippet(tc.window); got != tc.want {
				t.Fatalf("extractSearchSnippet = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractSearchSnippetIsBounded(t *testing.T) {
	long := strings.Repeat("word ", 400)
	snippet := extractSearchSnippet(`<div class="result__snippet">` + long + `</div>`)
	if len([]rune(snippet)) > webSearchMaxSnippetRunes+3 {
		t.Fatalf("snippet not bounded: %d runes", len([]rune(snippet)))
	}
	if !strings.HasSuffix(snippet, "...") {
		t.Fatalf("expected truncation marker, got tail %q", snippet[max0(len(snippet)-10):])
	}

	// An element that is never closed must not swallow the rest of the window.
	unclosed := extractSearchSnippet(`<div class="result__snippet">` + strings.Repeat("x", webSearchSnippetWindowBytes*2))
	if len([]rune(unclosed)) > webSearchMaxSnippetRunes+3 {
		t.Fatalf("unclosed snippet not bounded: %d runes", len([]rune(unclosed)))
	}
}

func TestParseDuckDuckGoHTMLResultsBoundsTitles(t *testing.T) {
	longTitle := strings.Repeat("t", webSearchMaxTitleRunes*2)
	body := `<div class="result"><a class="result__a" href="https://example.com/long">` + longTitle + `</a></div>`
	results := parseDuckDuckGoHTMLResults(body, 5)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %+v", results)
	}
	if len([]rune(results[0].Title)) > webSearchMaxTitleRunes+3 {
		t.Fatalf("title not bounded: %d runes", len([]rune(results[0].Title)))
	}
}

func TestSanitizeSearchText(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "ansi escape stripped", raw: "Clean\x1b[31m red", want: "Clean[31m red"},
		{name: "bidi override stripped", raw: "left‮right", want: "leftright"},
		{name: "zero width stripped", raw: "ze​ro", want: "zero"},
		{name: "newlines collapse", raw: "one\ntwo\r\n\tthree", want: "one two three"},
		{name: "nbsp collapses", raw: "a b", want: "a b"},
		{name: "already clean", raw: "plain text", want: "plain text"},
		{name: "empty", raw: "   ", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeSearchText(tc.raw); got != tc.want {
				t.Fatalf("sanitizeSearchText(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSanitizedTitlesReachParsedResults(t *testing.T) {
	body := "<div class=\"result\"><a class=\"result__a\" href=\"https://example.com/a\">Ti\x1btle‮</a></div>"
	results := parseDuckDuckGoHTMLResults(body, 5)
	if len(results) != 1 {
		t.Fatalf("expected one result, got %+v", results)
	}
	if strings.ContainsRune(results[0].Title, 0x1b) || strings.ContainsRune(results[0].Title, 0x202e) {
		t.Fatalf("control runes survived sanitization: %q", results[0].Title)
	}
}

func TestBoundText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "under limit", in: "abc", max: 10, want: "abc"},
		{name: "at limit", in: "abc", max: 3, want: "abc"},
		{name: "over limit", in: "abcdef", max: 3, want: "abc..."},
		{name: "trims trailing space", in: "ab cd", max: 3, want: "ab..."},
		{name: "multibyte safe", in: "héllo wörld", max: 5, want: "héllo..."},
		{name: "zero max", in: "abc", max: 0, want: ""},
		{name: "empty input", in: "", max: 5, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := boundText(tc.in, tc.max); got != tc.want {
				t.Fatalf("boundText(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

func TestFormatWebSearchResultsEmptyAndPopulated(t *testing.T) {
	if got := formatWebSearchResults("nothing", nil); got != "No search results found for nothing" {
		t.Fatalf("unexpected empty rendering: %q", got)
	}
	formatted := formatWebSearchResults("go", []webSearchResult{
		{Title: "A", URL: "https://a.example/", Snippet: "first"},
		{Title: "B", URL: "https://b.example/"},
	})
	for _, want := range []string{"Search results for go", "1. A", "URL: https://a.example/", "Snippet: first", "2. B"} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("expected %q in:\n%s", want, formatted)
		}
	}
	if strings.Contains(formatted, "Snippet: \n") {
		t.Fatalf("empty snippet should be omitted:\n%s", formatted)
	}
}

// Every case here fails before any socket is opened, so the test makes no
// network calls.
func TestWebSearchExecuteRejectsBadInputWithoutNetwork(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "blank query", input: `{"query":"  "}`, want: "query is required"},
		{name: "missing query", input: `{}`, want: "query is required"},
		{name: "oversized query", input: `{"query":"` + strings.Repeat("q", webSearchMaxQueryLen+1) + `"}`, want: "query is too long"},
		{name: "unknown field", input: `{"query":"go","depth":3}`, want: "unknown field"},
		{name: "forbidden host field", input: `{"query":"go","cwd":"C:\\tmp"}`, want: "not allowed in tool input"},
		{name: "wrong type", input: `{"query":123}`, want: "cannot unmarshal"},
		{name: "trailing data", input: `{"query":"go"} {}`, want: "trailing data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := (WebSearchTool{}).Execute(context.Background(), Call{ID: "ws", Name: "WebSearch", Input: json.RawMessage(tc.input)}, Env{})
			if err != nil {
				t.Fatalf("unexpected infrastructure error: %v", err)
			}
			if !result.IsError {
				t.Fatalf("expected an error result, got %+v", result)
			}
			if !strings.Contains(result.Output, tc.want) {
				t.Fatalf("expected output to contain %q, got %q", tc.want, result.Output)
			}
		})
	}
}

func TestWebSearchToolMetadata(t *testing.T) {
	tool := WebSearchTool{}
	if tool.Name() != "WebSearch" {
		t.Fatalf("unexpected name %q", tool.Name())
	}
	if tool.Risk(nil) != RiskRead {
		t.Fatalf("WebSearch must stay a read-risk tool, got %v", tool.Risk(nil))
	}
	if _, ok := tool.Schema().(webSearchInput); !ok {
		t.Fatalf("unexpected schema type %T", tool.Schema())
	}
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
