package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"autoto/internal/config"
	"autoto/internal/network"
)

// providerBaseURLOptions scopes the plain-HTTP exception to the single provider
// that opted in, so every other provider keeps the default policy.
func providerBaseURLOptions(cfg config.ProviderConfig) []network.Option {
	if !cfg.AllowPlaintextHTTP {
		return nil
	}
	return []network.Option{network.WithPlaintextHTTPAllowed()}
}

func validateProviderRuntimeConfig(cfg config.ProviderConfig) error {
	if err := validateProviderRuntimeIdentity(cfg); err != nil {
		return err
	}
	if cfg.Type != config.ProviderTypeCodex && strings.TrimSpace(cfg.BaseURL) != "" {
		if err := network.ValidateProviderBaseURL(context.Background(), cfg.BaseURL, providerBaseURLOptions(cfg)...); err != nil {
			return err
		}
	}
	if strings.TrimSpace(cfg.ProxyURL) != "" {
		if err := network.ValidateProviderProxyURL(context.Background(), cfg.ProxyURL); err != nil {
			return err
		}
	}
	if strings.ContainsAny(cfg.UserAgent, "\x00\r\n") {
		return errors.New("provider user agent is invalid")
	}
	_, err := providerRequestHeaders(cfg)
	return err
}

func providerHTTPClient(cfg config.ProviderConfig, timeout time.Duration) (*http.Client, error) {
	headers, err := providerRequestHeaders(cfg)
	if err != nil {
		return network.NewProviderHTTPClient(timeout), err
	}
	client, clientErr := network.NewConfiguredProviderHTTPClient(timeout, network.ProviderHTTPConfig{
		ProxyURL:              cfg.ProxyURL,
		ProxyUsername:         cfg.ProxyUsername,
		ProxyPassword:         cfg.ProxyPassword,
		UserAgent:             cfg.UserAgent,
		Headers:               headers,
		InsecureSkipTLSVerify: cfg.InsecureSkipTLSVerify,
	}, providerBaseURLOptions(cfg)...)
	if clientErr != nil {
		return network.NewProviderHTTPClient(timeout), clientErr
	}
	return client, nil
}

func providerRequestHeaders(cfg config.ProviderConfig) (http.Header, error) {
	headers := make(http.Header, len(cfg.RequestHeaders))
	seen := make(map[string]struct{}, len(cfg.RequestHeaders))
	for _, item := range cfg.RequestHeaders {
		name := http.CanonicalHeaderKey(strings.TrimSpace(item.Name))
		if name == "" || strings.ContainsAny(name, "\x00\r\n: \t") {
			return nil, errors.New("provider request header name is invalid")
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("provider request header %q is duplicated", name)
		}
		seen[key] = struct{}{}
		if item.Value == "" || strings.ContainsAny(item.Value, "\x00\r\n") {
			return nil, fmt.Errorf("provider request header %q value is invalid", name)
		}
		headers.Set(name, item.Value)
	}
	return headers, nil
}
