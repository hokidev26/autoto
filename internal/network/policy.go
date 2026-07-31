package network

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"
)

// Policy is an explicit outbound HTTP transport policy.
type Policy string

const (
	PolicyEnvironmentProxy Policy = "environment_proxy"
	PolicyPublicDirect     Policy = "public_direct"
	PolicyPrivateLANDirect Policy = "private_lan_direct"
	// PolicyProviderDirect allows only public HTTPS destinations or HTTP(S)
	// loopback endpoints. It is intended for user-configurable provider URLs.
	PolicyProviderDirect Policy = "provider_direct"
	// PolicyProviderProxy allows a user-configured proxy on a public, private-LAN,
	// or loopback address while continuing to deny metadata and special endpoints.
	PolicyProviderProxy Policy = "provider_proxy"
)

const (
	defaultLookupTimeout = 5 * time.Second
	defaultRedirectLimit = 10
	diagnosticTimeout    = 10 * time.Second
)

var (
	ErrInvalidPolicy      = errors.New("network: invalid transport policy")
	ErrInvalidURL         = errors.New("network: invalid destination")
	ErrDestinationDenied  = errors.New("network: destination denied by policy")
	ErrNameResolution     = errors.New("network: name resolution failed")
	ErrConnectionFailed   = errors.New("network: connection failed")
	ErrProxyConfiguration = errors.New("network: proxy configuration failed")
	ErrRedirectLimit      = errors.New("network: redirect limit reached")
	ErrRedirectDenied     = errors.New("network: redirects are denied by policy")
	ErrInvalidTarget      = errors.New("network: invalid diagnostic target")
)

// Resolver is the DNS surface used by direct policies. LookupIPAddr is used so
// tests and callers can supply a deterministic resolver without changing the
// process-wide resolver.
type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// DialContextFunc matches net.Dialer's DialContext method.
type DialContextFunc func(context.Context, string, string) (net.Conn, error)

// ProxyFunc matches http.Transport.Proxy.
type ProxyFunc func(*http.Request) (*url.URL, error)

// Option customizes transports and diagnostics. Direct policies always ignore
// proxy options and keep http.Transport.Proxy nil.
type Option func(*options)

type options struct {
	resolver       Resolver
	dialContext    DialContextFunc
	proxy          ProxyFunc
	lookupTimeout  time.Duration
	redirectLimit  int
	requestTimeout time.Duration
	now            func() time.Time
	// allowPlaintextHTTP relaxes PolicyProviderDirect for one explicitly
	// opted-in destination. It defaults to false so every other caller keeps
	// the "public HTTPS or loopback HTTP" guarantee.
	allowPlaintextHTTP bool
}

func defaultOptions() options {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return options{
		resolver:       net.DefaultResolver,
		dialContext:    dialer.DialContext,
		proxy:          http.ProxyFromEnvironment,
		lookupTimeout:  defaultLookupTimeout,
		redirectLimit:  defaultRedirectLimit,
		requestTimeout: diagnosticTimeout,
		now:            time.Now,
	}
}

func applyOptions(opts ...Option) options {
	cfg := defaultOptions()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.resolver == nil {
		cfg.resolver = net.DefaultResolver
	}
	if cfg.dialContext == nil {
		cfg.dialContext = defaultOptions().dialContext
	}
	if cfg.proxy == nil {
		cfg.proxy = http.ProxyFromEnvironment
	}
	if cfg.lookupTimeout <= 0 {
		cfg.lookupTimeout = defaultLookupTimeout
	}
	if cfg.redirectLimit <= 0 {
		cfg.redirectLimit = defaultRedirectLimit
	}
	if cfg.requestTimeout <= 0 {
		cfg.requestTimeout = diagnosticTimeout
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	return cfg
}

// WithResolver injects DNS resolution for direct policies.
func WithResolver(resolver Resolver) Option {
	return func(cfg *options) {
		if resolver != nil {
			cfg.resolver = resolver
		}
	}
}

// WithDialContext injects the final TCP dial operation. Direct policies pass a
// validated literal IP address to this function, never the original hostname.
func WithDialContext(dial DialContextFunc) Option {
	return func(cfg *options) {
		if dial != nil {
			cfg.dialContext = dial
		}
	}
}

// WithEnvironmentProxy injects proxy discovery for environment_proxy. It has
// no effect on either direct policy.
func WithEnvironmentProxy(proxy ProxyFunc) Option {
	return func(cfg *options) {
		if proxy != nil {
			cfg.proxy = proxy
		}
	}

}

// WithLookupTimeout bounds each direct DNS lookup.
func WithLookupTimeout(timeout time.Duration) Option {
	return func(cfg *options) {
		if timeout > 0 {
			cfg.lookupTimeout = timeout
		}
	}

}

// WithRedirectLimit sets the maximum redirects accepted by RedirectPolicy.
func WithRedirectLimit(limit int) Option {
	return func(cfg *options) {
		if limit > 0 {
			cfg.redirectLimit = limit
		}
	}

}

// WithDiagnosticTimeout sets the total timeout for a diagnostic request.
func WithDiagnosticTimeout(timeout time.Duration) Option {
	return func(cfg *options) {
		if timeout > 0 {
			cfg.requestTimeout = timeout
		}
	}

}

// WithPlaintextHTTPAllowed lets one explicitly opted-in destination be reached
// over plain HTTP even when it does not resolve to loopback. It only affects
// PolicyProviderDirect.
//
// Callers must surface the exposure to the user before enabling this: the API
// key and every request and response body travel in cleartext, readable by any
// hop on the path. It exists so a user can reach a relay that has no TLS
// certificate, scoped to that single provider rather than the whole process.
func WithPlaintextHTTPAllowed() Option {
	return func(cfg *options) {
		cfg.allowPlaintextHTTP = true
	}
}

// withClock is intentionally package-private; production diagnostics always use
// the system clock while tests can make latency deterministic.
func withClock(now func() time.Time) Option {
	return func(cfg *options) {
		if now != nil {
			cfg.now = now
		}
	}

}

func validPolicy(policy Policy) bool {
	switch policy {
	case PolicyEnvironmentProxy, PolicyPublicDirect, PolicyPrivateLANDirect, PolicyProviderDirect, PolicyProviderProxy:
		return true
	default:
		return false
	}
}
