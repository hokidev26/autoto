package tools

import (
	"strings"
	"testing"
)

// The four structures below were each lost by the previous regex pipeline. They
// are the ones a coding agent reads documentation for, so each has a test that
// fails if the extraction flattens it again.

func TestHTMLToTextKeepsPreformattedBlockIndentation(t *testing.T) {
	text := htmlToText("<body><p>Example</p><pre><code>func main() {\n\tfmt.Println(\"hi\")\n}</code></pre></body>")
	if !strings.Contains(text, "```") {
		t.Fatalf("expected a fenced block, got %q", text)
	}
	// The indentation is the point: an example that loses it cannot be copied.
	if !strings.Contains(text, "\tfmt.Println(\"hi\")") {
		t.Fatalf("expected inner indentation preserved, got %q", text)
	}
	if !strings.Contains(text, "func main() {") {
		t.Fatalf("expected code text preserved, got %q", text)
	}
}

func TestHTMLToTextKeepsTableRowsOnOneLineEach(t *testing.T) {
	text := htmlToText(`<table>
		<tr><th>Name</th><th>Type</th><th>Required</th></tr>
		<tr><td>limit</td><td>int</td><td>no</td></tr>
		<tr><td>url</td><td>string</td><td>yes</td></tr>
	</table>`)
	for _, want := range []string{"Name | Type | Required", "limit | int | no", "url | string | yes"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected row %q in %q", want, text)
		}
	}
}

func TestHTMLToTextKeepsLinkTargets(t *testing.T) {
	text := htmlToText(`<p>See <a href="https://example.com/api/auth">the auth guide</a> first.</p>`)
	if !strings.Contains(text, "the auth guide (https://example.com/api/auth)") {
		t.Fatalf("expected link text and target, got %q", text)
	}
}

func TestHTMLToTextKeepsHeadingLevels(t *testing.T) {
	text := htmlToText("<h1>API</h1><h2>Authentication</h2><h3>Rotating a key</h3>")
	for _, want := range []string{"# API", "## Authentication", "### Rotating a key"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in %q", want, text)
		}
	}
}

func TestHTMLToTextDropsSiteFurniture(t *testing.T) {
	text := htmlToText(`<body>
		<header><a href="/">Home</a><a href="/pricing">Pricing</a></header>
		<nav><ul><li><a href="/a">Guides</a></li><li><a href="/b">Reference</a></li></ul></nav>
		<aside>Subscribe to our newsletter</aside>
		<main><p>The actual answer.</p></main>
		<footer>Copyright 2026 Example Inc</footer>
	</body>`)
	if !strings.Contains(text, "The actual answer.") {
		t.Fatalf("expected article content kept, got %q", text)
	}
	for _, unwanted := range []string{"Pricing", "newsletter", "Copyright", "Reference"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("expected %q dropped as site furniture, got %q", unwanted, text)
		}
	}
}

// Real pages nest furniture: a <header> almost always wraps a <nav>. Matching
// all furniture tags in one alternation with a non-greedy body matched from
// <header> to the inner </nav>, which deleted a fragment and left the remaining
// header content behind as text. This pins the nested case.
func TestHTMLToTextDropsNestedSiteFurniture(t *testing.T) {
	text := htmlToText(`<body>
		<header class="site">
			<div class="brand">BrandName</div>
			<nav><a href="/pricing">Pricing</a><a href="/blog">Blog</a></nav>
			<div class="social">FollowUsHere</div>
		</header>
		<main><p>Documented behaviour.</p></main>
	</body>`)
	if !strings.Contains(text, "Documented behaviour.") {
		t.Fatalf("expected content kept, got %q", text)
	}
	for _, unwanted := range []string{"BrandName", "Pricing", "Blog", "FollowUsHere"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("expected nested furniture %q dropped, got %q", unwanted, text)
		}
	}
}

// A link whose visible text is already the URL should not be doubled, and an
// in-page or javascript: target carries no address worth keeping.
func TestHTMLToTextDoesNotDuplicateOrKeepUselessHrefs(t *testing.T) {
	text := htmlToText(`<p><a href="https://example.com">https://example.com</a></p>`)
	if strings.Count(text, "https://example.com") != 1 {
		t.Fatalf("expected the URL once, got %q", text)
	}
	text = htmlToText(`<p><a href="#section-2">Jump to section 2</a></p>`)
	if strings.Contains(text, "#section-2") {
		t.Fatalf("expected in-page anchor target dropped, got %q", text)
	}
	if !strings.Contains(text, "Jump to section 2") {
		t.Fatalf("expected anchor text kept, got %q", text)
	}
	text = htmlToText(`<p><a href="javascript:void(0)">Toggle</a></p>`)
	if strings.Contains(text, "javascript:") {
		t.Fatalf("expected javascript: target dropped, got %q", text)
	}
}

// Scripts and styles must still go, including inside content that now survives.
func TestHTMLToTextStillRemovesScriptsAndStyles(t *testing.T) {
	text := htmlToText(`<body><style>.a{color:red}</style><script>alert(1)</script><h2>Title</h2><p>Body</p></body>`)
	if strings.Contains(text, "alert") || strings.Contains(text, "color:red") {
		t.Fatalf("expected scripts and styles removed, got %q", text)
	}
	if !strings.Contains(text, "## Title") || !strings.Contains(text, "Body") {
		t.Fatalf("expected content kept, got %q", text)
	}
}

// The placeholder used to protect preformatted blocks must never reach output,
// including when a page contains no such block or an empty one.
func TestHTMLToTextLeaksNoPlaceholder(t *testing.T) {
	for _, body := range []string{
		"<p>plain</p>",
		"<pre></pre><p>after</p>",
		"<pre>   </pre><p>after</p>",
		"<pre>one</pre><pre>two</pre>",
	} {
		text := htmlToText(body)
		if strings.Contains(text, "PRE") && strings.Contains(text, "\x00") {
			t.Fatalf("placeholder leaked for %q: %q", body, text)
		}
		if strings.Contains(text, "\x00") {
			t.Fatalf("NUL leaked for %q: %q", body, text)
		}
	}
}
