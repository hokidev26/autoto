package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const webFetchMaxBytes = 1 << 20
const webFetchDefaultLimit = 20000
const webFetchMaxLimit = 100000
const webFetchMaxRedirects = 10

type webFetchResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type webFetchDialContext func(context.Context, string, string) (net.Conn, error)

type WebFetchTool struct{}

type webFetchInput struct {
	URL     string `json:"url" desc:"Public http or https URL. Private, loopback, and cloud metadata addresses are rejected, and redirects are revalidated against the same rules."`
	Limit   int    `json:"limit,omitempty" jsonschema:"minimum=1" desc:"Maximum bytes of extracted text to return."`
	Timeout int    `json:"timeout,omitempty" jsonschema:"minimum=1" desc:"Request timeout in milliseconds. Defaults to 15000."`
}

func (WebFetchTool) Name() string { return "WebFetch" }
func (WebFetchTool) Description() string {
	return "Fetch a public HTTP(S) URL and return simplified text for documentation lookup."
}
func (WebFetchTool) Schema() any               { return webFetchInput{} }
func (WebFetchTool) Risk(json.RawMessage) Risk { return RiskRead }

func (WebFetchTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input webFetchInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	target, err := validatePublicFetchURL(ctx, input.URL)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	timeout := time.Duration(input.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "Autoto-WebFetch/0.1")
	req.Header.Set("Accept", "text/html,text/plain,application/xhtml+xml,application/json;q=0.8,*/*;q=0.1")
	client := newWebFetchHTTPClient(timeout, net.DefaultResolver, nil)
	resp, err := client.Do(req)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{Output: fmt.Sprintf("fetch failed with status %s", resp.Status), IsError: true, Meta: map[string]any{"status": resp.StatusCode, "url": target.String()}}, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxBytes+1))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	truncatedBody := len(data) > webFetchMaxBytes
	if truncatedBody {
		data = data[:webFetchMaxBytes]
	}
	text := simplifyFetchedContent(resp.Header.Get("Content-Type"), string(data))
	limit := input.Limit
	if limit <= 0 {
		limit = webFetchDefaultLimit
	}
	if limit > webFetchMaxLimit {
		limit = webFetchMaxLimit
	}
	out, truncatedText := truncate(text, limit)
	return Result{Output: out, Meta: map[string]any{"url": target.String(), "status": resp.StatusCode, "contentType": resp.Header.Get("Content-Type"), "truncated": truncatedBody || truncatedText}}, nil
}

func validatePublicFetchURL(ctx context.Context, raw string) (*url.URL, error) {
	return validatePublicFetchURLWithResolver(ctx, raw, net.DefaultResolver)
}

func validatePublicFetchURLWithResolver(ctx context.Context, raw string, resolver webFetchResolver) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("only http and https urls are supported")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, errors.New("url host is required")
	}
	if _, err := resolvePublicFetchHost(ctx, resolver, host); err != nil {
		return nil, err
	}
	return parsed, nil
}

func newWebFetchHTTPClient(timeout time.Duration, resolver webFetchResolver, dial webFetchDialContext) *http.Client {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if dial == nil {
		netDialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		dial = netDialer.DialContext
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = newPublicFetchDialContext(resolver, dial)
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: webFetchRedirectPolicy(resolver),
	}
}

func webFetchRedirectPolicy(resolver webFetchResolver) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= webFetchMaxRedirects {
			return fmt.Errorf("stopped after %d redirects", webFetchMaxRedirects)
		}
		if _, err := validatePublicFetchURLWithResolver(req.Context(), req.URL.String(), resolver); err != nil {
			return fmt.Errorf("redirect target rejected: %w", err)
		}
		return nil
	}
}

func newPublicFetchDialContext(resolver webFetchResolver, dial webFetchDialContext) webFetchDialContext {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid dial address: %w", err)
		}
		ips, err := resolvePublicFetchHost(ctx, resolver, host)
		if err != nil {
			return nil, err
		}

		var lastErr error
		for _, ip := range ips {
			if network == "tcp4" && ip.To4() == nil {
				continue
			}
			if network == "tcp6" && ip.To4() != nil {
				continue
			}
			// Dial the validated literal IP so DNS cannot change between validation and connect.
			// The request URL is unchanged, so net/http keeps the original Host header and TLS SNI.
			conn, dialErr := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, fmt.Errorf("no public IP addresses available for %s", host)
	}
}

func resolvePublicFetchHost(ctx context.Context, resolver webFetchResolver, host string) ([]net.IP, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("url host is required")
	}
	if isLocalHostname(host) || strings.Contains(host, "%") {
		return nil, errors.New("local/private hosts are not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateOrLocalIP(ip) {
			return nil, errors.New("local/private hosts are not allowed")
		}
		return []net.IP{ip}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := resolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve host: %w", err)
	}
	if len(addrs) == 0 {
		return nil, errors.New("resolve host: no IP addresses")
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if isPrivateOrLocalIP(addr.IP) {
			return nil, errors.New("local/private hosts are not allowed")
		}
		ips = append(ips, addr.IP)
	}
	return ips, nil
}

func isLocalHostname(host string) bool {
	host = strings.TrimRight(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

var webFetchBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fec0::/10"),
}

func isPrivateOrLocalIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	for _, prefix := range webFetchBlockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func simplifyFetchedContent(contentType, body string) string {
	lower := strings.ToLower(contentType)
	if strings.Contains(lower, "html") || strings.Contains(strings.ToLower(body[:min(len(body), 512)]), "<html") {
		return htmlToText(body)
	}
	return strings.TrimSpace(body)
}

var (
	scriptRE   = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	styleRE    = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	noscriptRE = regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`)
	// Site furniture, dropped with its contents. On a documentation page the
	// sidebar, breadcrumb, cookie banner, and footer link farm are often larger
	// than the article, and they arrive first, so without this they consume the
	// caller's byte budget before the answer appears.
	//
	// One expression per tag, each closing against itself. A single alternation
	// with a non-greedy body is wrong here: a <header> containing a <nav> would
	// match from <header> to the inner </nav>, deleting a fragment and leaving
	// the rest of the header behind as text. Real pages nest these constantly.
	chromeREs = buildChromeMatchers()
	// pre is deliberately absent here: its contents are extracted first and its
	// internal newlines must survive, which this rule would destroy.
	tagBreakRE    = regexp.MustCompile(`(?i)</?(p|br|div|section|article|main|li|ul|ol|tr|table|blockquote|dl|dt|dd)[^>]*>`)
	headingRE     = regexp.MustCompile(`(?is)<h([1-6])[^>]*>(.*?)</h[1-6]>`)
	preRE         = regexp.MustCompile(`(?is)<pre[^>]*>(.*?)</pre>`)
	inlineCodeRE  = regexp.MustCompile(`(?is)<code[^>]*>(.*?)</code>`)
	anchorRE      = regexp.MustCompile(`(?is)<a\b[^>]*\shref\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))[^>]*>(.*?)</a>`)
	cellRE        = regexp.MustCompile(`(?i)</(td|th)>`)
	rowEndRE      = regexp.MustCompile(`(?i)</tr>`)
	tagRE         = regexp.MustCompile(`(?s)<[^>]+>`)
	blankLinesRE  = regexp.MustCompile(`\n{3,}`)
	spacesRE      = regexp.MustCompile(`[ \t\f\r]+`)
	trailingBarRE = regexp.MustCompile(`(?m)\s*\|\s*$`)
)

// htmlToText renders HTML as plain text for a model to read. It is a lossy
// extractor rather than a parser, and stays regex-based on purpose: the
// standard library has no HTML tokenizer, so parsing properly would mean adding
// golang.org/x/net, which is not currently in the module graph in any form.
//
// The structures below are kept because they are the ones a coding agent reads
// documentation for. Flattening them does not merely lose formatting, it loses
// the answer: a stripped table turns a parameter reference into one unreadable
// line, a stripped <pre> makes an indentation-sensitive example unusable, and a
// stripped anchor leaves the link text with no address to follow.
// htmlToText renders a whole document, so it keeps the structures a reader needs.
func htmlToText(body string) string {
	return htmlToTextWithOptions(body, true)
}

// htmlToTextSnippet renders a fragment that has to survive as a single line of
// prose. Search results reuse this conversion for their one-line summaries, and
// there a code fence is noise rather than structure: it would wrap a few words of
// example code in ``` inside a sentence.
func htmlToTextSnippet(body string) string {
	return htmlToTextWithOptions(body, false)
}

func htmlToTextWithOptions(body string, fenceCode bool) string {
	body = scriptRE.ReplaceAllString(body, "")
	body = styleRE.ReplaceAllString(body, "")
	body = noscriptRE.ReplaceAllString(body, "")
	body = stripSiteFurniture(body)

	// Preformatted blocks are lifted out before any other rule can touch them,
	// held aside under a marker, and restored at the end as fenced code. The
	// marker uses a rune that cannot appear in HTML text at this stage, so it
	// cannot collide with page content.
	var blocks []string
	body = preRE.ReplaceAllStringFunc(body, func(match string) string {
		inner := preRE.FindStringSubmatch(match)[1]
		inner = inlineCodeRE.ReplaceAllString(inner, "$1")
		inner = tagRE.ReplaceAllString(inner, "")
		inner = html.UnescapeString(inner)
		blocks = append(blocks, strings.Trim(inner, "\n"))
		return "\n\x00PRE" + strconv.Itoa(len(blocks)-1) + "\x00\n"
	})

	// Heading level carries the document outline, which is how a reader finds the
	// relevant section in a long reference page.
	body = headingRE.ReplaceAllStringFunc(body, func(match string) string {
		m := headingRE.FindStringSubmatch(match)
		level, err := strconv.Atoi(m[1])
		if err != nil || level < 1 || level > 6 {
			level = 1
		}
		return "\n\n" + strings.Repeat("#", level) + " " + strings.TrimSpace(tagRE.ReplaceAllString(m[2], "")) + "\n\n"
	})

	// Keep the address, not just the label. Anchors whose text already equals the
	// href, or which have no visible text, would only produce noise.
	body = anchorRE.ReplaceAllStringFunc(body, func(match string) string {
		m := anchorRE.FindStringSubmatch(match)
		href := strings.TrimSpace(firstNonEmpty(m[2], m[3], m[4]))
		text := strings.TrimSpace(tagRE.ReplaceAllString(m[5], ""))
		text = strings.TrimSpace(spacesRE.ReplaceAllString(html.UnescapeString(text), " "))
		switch {
		case text == "":
			return " "
		case href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:"):
			return text
		case text == href:
			return text
		default:
			return text + " (" + href + ")"
		}
	})

	// Table cells become bar-separated so a row stays one readable line instead
	// of collapsing into a run of unlabelled values.
	body = cellRE.ReplaceAllString(body, " | ")
	body = rowEndRE.ReplaceAllString(body, "\n")

	body = inlineCodeRE.ReplaceAllString(body, "`$1`")
	body = tagBreakRE.ReplaceAllString(body, "\n")
	body = tagRE.ReplaceAllString(body, "")
	body = html.UnescapeString(body)

	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(spacesRE.ReplaceAllString(line, " "))
		line = trailingBarRE.ReplaceAllString(line, "")
		if line != "" {
			out = append(out, line)
		}
	}
	text := blankLinesRE.ReplaceAllString(strings.Join(out, "\n"), "\n\n")

	for i, block := range blocks {
		replacement := block
		if fenceCode {
			replacement = "```\n" + block + "\n```"
		}
		if strings.TrimSpace(block) == "" {
			replacement = ""
		}
		text = strings.ReplaceAll(text, "\x00PRE"+strconv.Itoa(i)+"\x00", replacement)
	}
	return strings.TrimSpace(blankLinesRE.ReplaceAllString(text, "\n\n"))
}

// siteFurnitureTags are containers whose contents are navigation or chrome
// rather than the page's answer. form is included because search boxes and
// newsletter signups otherwise contribute stray labels and button text.
var siteFurnitureTags = []string{"nav", "aside", "header", "footer", "form", "svg", "dialog"}

func buildChromeMatchers() []*regexp.Regexp {
	matchers := make([]*regexp.Regexp, 0, len(siteFurnitureTags))
	for _, tag := range siteFurnitureTags {
		matchers = append(matchers, regexp.MustCompile(`(?is)<`+tag+`\b[^>]*>.*?</`+tag+`>`))
	}
	return matchers
}

// stripSiteFurniture removes each furniture container together with its
// contents. It repeats until nothing more matches, because the innermost
// element of a nested pair is the one a non-greedy match finds first, and
// removing it is what exposes the outer one.
func stripSiteFurniture(body string) string {
	const maxPasses = 6
	for pass := 0; pass < maxPasses; pass++ {
		before := body
		for _, matcher := range chromeREs {
			body = matcher.ReplaceAllString(body, "\n")
		}
		if body == before {
			break
		}
	}
	return body
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
