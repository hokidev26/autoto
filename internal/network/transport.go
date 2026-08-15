package network

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

var publicBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fec0::/10"),
}

var metadataAddresses = []netip.Addr{
	netip.MustParseAddr("100.100.100.200"),
	netip.MustParseAddr("169.254.0.23"),
	netip.MustParseAddr("169.254.169.254"),
	netip.MustParseAddr("169.254.170.2"),
	netip.MustParseAddr("192.0.0.192"),
	netip.MustParseAddr("fd00:ec2::254"),
}

var metadataHostnames = []string{
	"instance-data",
	"instance-data.ec2.internal",
	"metadata",
	"metadata.google.internal",
	"metadata.goog",
}

// NewEnvironmentProxyTransport constructs a transport that honors environment
// proxy discovery and otherwise uses the standard net/http transport defaults.
func NewEnvironmentProxyTransport(opts ...Option) *http.Transport {
	cfg := applyOptions(opts...)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = redactingProxy(cfg.proxy)
	transport.DialContext = redactingDial(cfg.dialContext)
	return transport
}

// NewPublicDirectTransport constructs a proxy-free transport that only dials
// validated public addresses. DNS answers are checked as a set and the selected
// literal address is passed to the dialer, closing the validation/dial rebinding
// window while preserving the request Host header and TLS SNI.
func NewPublicDirectTransport(opts ...Option) *http.Transport {
	cfg := applyOptions(opts...)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = directDialContext(PolicyPublicDirect, cfg)
	return transport
}

// NewPrivateLANDirectTransport constructs a proxy-free transport that only
// dials loopback, private, or link-local destinations. Metadata endpoints stay
// denied even though some use link-local addresses.
func NewPrivateLANDirectTransport(opts ...Option) *http.Transport {
	cfg := applyOptions(opts...)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = directDialContext(PolicyPrivateLANDirect, cfg)
	return transport
}

// NewProviderDirectTransport constructs a proxy-free transport for configurable
// provider endpoints. It permits either public HTTPS targets or loopback HTTP(S)
// targets, with DNS answers pinned to validated literal addresses.
func NewProviderDirectTransport(opts ...Option) *http.Transport {
	cfg := applyOptions(opts...)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = directDialContext(PolicyProviderDirect, cfg)
	return transport
}

// NewTransport constructs one of the explicit outbound policies.
func NewTransport(policy Policy, opts ...Option) (*http.Transport, error) {
	switch policy {
	case PolicyEnvironmentProxy:
		return NewEnvironmentProxyTransport(opts...), nil
	case PolicyPublicDirect:
		return NewPublicDirectTransport(opts...), nil
	case PolicyPrivateLANDirect:
		return NewPrivateLANDirectTransport(opts...), nil
	case PolicyProviderDirect:
		return NewProviderDirectTransport(opts...), nil
	default:
		return nil, ErrInvalidPolicy
	}
}

// ValidateURL validates a destination before a request or redirect is sent.
// Errors are deliberately stable and contain no URL, hostname, query, proxy,
// credential, or resolver detail.
func ValidateURL(ctx context.Context, policy Policy, target *url.URL, opts ...Option) error {
	return validateURL(ctx, policy, target, applyOptions(opts...))
}

// RedirectPolicy revalidates every redirect destination under the same policy.
func RedirectPolicy(policy Policy, opts ...Option) func(*http.Request, []*http.Request) error {
	cfg := applyOptions(opts...)
	return redirectPolicy(policy, cfg)
}

func redirectPolicy(policy Policy, cfg options) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if !validPolicy(policy) {
			return ErrInvalidPolicy
		}
		if len(via) >= cfg.redirectLimit {
			return ErrRedirectLimit
		}
		if req == nil {
			return ErrInvalidURL
		}
		return validateURL(req.Context(), policy, req.URL, cfg)
	}
}

func newHTTPClient(policy Policy, cfg options) (*http.Client, error) {
	transport, err := NewTransport(policy,
		WithResolver(cfg.resolver),
		WithDialContext(cfg.dialContext),
		WithEnvironmentProxy(cfg.proxy),
		WithLookupTimeout(cfg.lookupTimeout),
	)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       cfg.requestTimeout,
		CheckRedirect: redirectPolicy(policy, cfg),
	}, nil
}

func validateURL(ctx context.Context, policy Policy, target *url.URL, cfg options) error {
	if !validPolicy(policy) {
		return ErrInvalidPolicy
	}
	if target == nil || !target.IsAbs() || target.Opaque != "" || target.Host == "" || target.Hostname() == "" || target.User != nil {
		return ErrInvalidURL
	}
	if strings.ContainsAny(target.Host, "\x00\r\n") {
		return ErrInvalidURL
	}
	scheme := strings.ToLower(target.Scheme)
	if scheme != "http" && scheme != "https" {
		return ErrInvalidURL
	}
	if port := target.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return ErrInvalidURL
		}
	}
	if policy == PolicyEnvironmentProxy {
		return nil
	}
	addresses, err := resolveAllowed(ctx, policy, target.Hostname(), cfg)
	if err != nil {
		return err
	}
	// Plain HTTP would put the API key and every payload on the wire in the
	// clear, so it stays limited to loopback unless the caller opted in for this
	// specific destination.
	if policy == PolicyProviderDirect && scheme == "http" && !cfg.allowPlaintextHTTP && !allLoopbackAddresses(addresses) {
		return ErrDestinationDenied
	}
	return nil
}

func redactingProxy(proxy ProxyFunc) ProxyFunc {
	return func(req *http.Request) (*url.URL, error) {
		proxyURL, err := proxy(req)
		if err != nil {
			return nil, ErrProxyConfiguration
		}
		if proxyURL == nil {
			return nil, nil
		}
		scheme := strings.ToLower(proxyURL.Scheme)
		if proxyURL.Host == "" || proxyURL.Hostname() == "" || proxyURL.Opaque != "" || proxyURL.Fragment != "" || proxyURL.RawQuery != "" {
			return nil, ErrProxyConfiguration
		}
		switch scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, ErrProxyConfiguration
		}
		clone := *proxyURL
		return &clone, nil
	}
}

func redactingDial(dial DialContextFunc) DialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, redactNetworkError(ctx, err, ErrConnectionFailed)
		}
		return conn, nil
	}
}

func directDialContext(policy Policy, cfg options) DialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" || port == "" {
			return nil, ErrInvalidURL
		}
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return nil, ErrInvalidURL
		}
		addresses, err := resolveAllowed(ctx, policy, host, cfg)
		if err != nil {
			return nil, err
		}

		attempted := false
		for _, resolved := range addresses {
			addr, ok := netip.AddrFromSlice(resolved.IP)
			if !ok {
				continue
			}
			addr = addr.Unmap()
			if network == "tcp4" && !addr.Is4() {
				continue
			}
			if network == "tcp6" && !addr.Is6() {
				continue
			}
			if network != "tcp" && network != "tcp4" && network != "tcp6" {
				return nil, ErrConnectionFailed
			}
			attempted = true
			dialHost := addr.String()
			if resolved.Zone != "" && addr.Is6() {
				dialHost += "%" + resolved.Zone
			}
			conn, dialErr := cfg.dialContext(ctx, network, net.JoinHostPort(dialHost, port))
			if dialErr == nil {
				return conn, nil
			}
			if ctx.Err() != nil {
				return nil, redactNetworkError(ctx, dialErr, ErrConnectionFailed)
			}
		}
		if !attempted {
			return nil, ErrConnectionFailed
		}
		return nil, ErrConnectionFailed
	}
}

func resolveAllowed(ctx context.Context, policy Policy, host string, cfg options) ([]net.IPAddr, error) {
	if policy != PolicyPublicDirect && policy != PolicyPrivateLANDirect && policy != PolicyProviderDirect && policy != PolicyProviderProxy {
		return nil, ErrInvalidPolicy
	}
	if host == "" || host != strings.TrimSpace(host) || strings.ContainsAny(host, "\x00\r\n") {
		return nil, ErrInvalidURL
	}

	lookupHost := strings.TrimRight(host, ".")
	if lookupHost == "" {
		return nil, ErrInvalidURL
	}
	lowerHost := strings.ToLower(lookupHost)
	if isMetadataHostname(lowerHost) {
		return nil, ErrDestinationDenied
	}
	if policy == PolicyPublicDirect && (isLocalHostname(lowerHost) || lowerHost == "local" || strings.HasSuffix(lowerHost, ".local")) {
		return nil, ErrDestinationDenied
	}
	if policy == PolicyProviderDirect && (lowerHost == "local" || strings.HasSuffix(lowerHost, ".local")) {
		return nil, ErrDestinationDenied
	}

	if literal, err := netip.ParseAddr(lookupHost); err == nil {
		zone := literal.Zone()
		literal = literal.Unmap()
		if zone != "" && !literal.Is6() {
			return nil, ErrInvalidURL
		}
		resolved := net.IPAddr{IP: net.IP(literal.AsSlice()), Zone: zone}
		if !addressAllowed(policy, literal) {
			return nil, ErrDestinationDenied
		}
		return []net.IPAddr{resolved}, nil
	} else if strings.ContainsAny(lookupHost, ":%") {
		return nil, ErrInvalidURL
	}

	lookupCtx, cancel := context.WithTimeout(ctx, cfg.lookupTimeout)
	defer cancel()
	addresses, err := cfg.resolver.LookupIPAddr(lookupCtx, lookupHost)
	if err != nil {
		return nil, redactNetworkError(lookupCtx, err, ErrNameResolution)
	}
	if len(addresses) == 0 {
		return nil, ErrNameResolution
	}

	validated := make([]net.IPAddr, 0, len(addresses))
	for _, resolved := range addresses {
		addr, ok := netip.AddrFromSlice(resolved.IP)
		if !ok {
			return nil, ErrNameResolution
		}
		addr = addr.Unmap()
		if resolved.Zone != "" && !addr.Is6() {
			return nil, ErrNameResolution
		}
		if !addressAllowed(policy, addr) {
			return nil, ErrDestinationDenied
		}
		validated = append(validated, net.IPAddr{IP: net.IP(addr.AsSlice()), Zone: resolved.Zone})
	}
	if policy == PolicyProviderDirect && !allProviderAddressesAllowed(validated) {
		return nil, ErrDestinationDenied
	}
	if policy == PolicyProviderProxy && !allProviderProxyAddressesAllowed(validated) {
		return nil, ErrDestinationDenied
	}
	return validated, nil
}

func addressAllowed(policy Policy, addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || isMetadataAddress(addr) || addr.IsUnspecified() || addr.IsMulticast() {
		return false
	}
	switch policy {
	case PolicyPublicDirect:
		if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
			return false
		}
		for _, prefix := range publicBlockedPrefixes {
			if prefix.Contains(addr) {
				return false
			}
		}
		return true
	case PolicyPrivateLANDirect:
		return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
	case PolicyProviderDirect:
		return addr.IsLoopback() || addressAllowed(PolicyPublicDirect, addr)
	case PolicyProviderProxy:
		return addressAllowed(PolicyPublicDirect, addr) || addressAllowed(PolicyPrivateLANDirect, addr)
	default:
		return false
	}
}

func allLoopbackAddresses(addresses []net.IPAddr) bool {
	if len(addresses) == 0 {
		return false
	}
	for _, resolved := range addresses {
		addr, ok := netip.AddrFromSlice(resolved.IP)
		if !ok || !addr.Unmap().IsLoopback() {
			return false
		}
	}
	return true
}

func allProviderAddressesAllowed(addresses []net.IPAddr) bool {
	if len(addresses) == 0 {
		return false
	}
	loopback := allLoopbackAddresses(addresses)
	if loopback {
		return true
	}
	for _, resolved := range addresses {
		addr, ok := netip.AddrFromSlice(resolved.IP)
		if !ok || !addressAllowed(PolicyPublicDirect, addr.Unmap()) {
			return false
		}
	}
	return true
}

func allProviderProxyAddressesAllowed(addresses []net.IPAddr) bool {
	if len(addresses) == 0 {
		return false
	}
	allPublic := true
	allPrivate := true
	for _, resolved := range addresses {
		addr, ok := netip.AddrFromSlice(resolved.IP)
		if !ok {
			return false
		}
		addr = addr.Unmap()
		allPublic = allPublic && addressAllowed(PolicyPublicDirect, addr)
		allPrivate = allPrivate && addressAllowed(PolicyPrivateLANDirect, addr)
	}
	return allPublic || allPrivate
}

func isLocalHostname(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func isMetadataHostname(host string) bool {
	for _, blocked := range metadataHostnames {
		if host == blocked || strings.HasSuffix(host, "."+blocked) {
			return true
		}
	}
	return false
}

// PublicDirectHostDenied reports whether PolicyPublicDirect would refuse this
// hostname before DNS, including cloud metadata names and localhost/.local.
func PublicDirectHostDenied(host string) bool {
	lookupHost := strings.TrimRight(strings.TrimSpace(host), ".")
	if lookupHost == "" {
		return true
	}
	lowerHost := strings.ToLower(lookupHost)
	if isMetadataHostname(lowerHost) {
		return true
	}
	return isLocalHostname(lowerHost) || lowerHost == "local" || strings.HasSuffix(lowerHost, ".local")
}

// PublicDirectAddressAllowed reports whether an IP may be dialed under
// PolicyPublicDirect (global unicast, not private/loopback/link-local/metadata).
func PublicDirectAddressAllowed(addr netip.Addr) bool {
	return addressAllowed(PolicyPublicDirect, addr)
}

func isMetadataAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, blocked := range metadataAddresses {
		if addr == blocked {
			return true
		}
	}
	return false
}

func redactNetworkError(ctx context.Context, err, fallback error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return fallback
}
