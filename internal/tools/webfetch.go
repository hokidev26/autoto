package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
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
		return nil, fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("only http and https urls are supported")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("url host is required")
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
		return nil, fmt.Errorf("url host is required")
	}
	if isLocalHostname(host) || strings.Contains(host, "%") {
		return nil, fmt.Errorf("local/private hosts are not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateOrLocalIP(ip) {
			return nil, fmt.Errorf("local/private hosts are not allowed")
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
		return nil, fmt.Errorf("resolve host: no IP addresses")
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if isPrivateOrLocalIP(addr.IP) {
			return nil, fmt.Errorf("local/private hosts are not allowed")
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
	scriptRE     = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	styleRE      = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	noscriptRE   = regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript>`)
	tagBreakRE   = regexp.MustCompile(`(?i)</?(p|br|div|section|article|header|footer|main|li|ul|ol|h[1-6]|tr|table|pre|blockquote)[^>]*>`)
	tagRE        = regexp.MustCompile(`(?s)<[^>]+>`)
	blankLinesRE = regexp.MustCompile(`\n{3,}`)
	spacesRE     = regexp.MustCompile(`[ \t\f\r]+`)
)

func htmlToText(body string) string {
	body = scriptRE.ReplaceAllString(body, "")
	body = styleRE.ReplaceAllString(body, "")
	body = noscriptRE.ReplaceAllString(body, "")
	body = tagBreakRE.ReplaceAllString(body, "\n")
	body = tagRE.ReplaceAllString(body, "")
	body = html.UnescapeString(body)
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(spacesRE.ReplaceAllString(line, " "))
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(blankLinesRE.ReplaceAllString(strings.Join(out, "\n"), "\n\n"))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
