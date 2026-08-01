package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const (
	ProviderAPIKeySourceNone         = "none"
	ProviderAPIKeySourceEnvironment  = "environment"
	ProviderAPIKeySourceLegacyConfig = "legacy_config"
)

// ProviderAPIKeyInput describes the trusted startup source of an already
// loaded Provider API key. Source is never serialized back to config.json.
type ProviderAPIKeyInput struct {
	Source              string
	APIKey              string
	LegacyConfigPresent bool
}

// InspectProviderAPIKeyInputs distinguishes API keys explicitly present in the
// raw config file from values inherited from environment-backed defaults. An
// explicitly configured environment variable always wins, even when an older
// config.json still contains a plaintext apiKey.
func InspectProviderAPIKeyInputs(path string, cfg Config) (map[string]ProviderAPIKeyInput, error) {
	resolved, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(resolved)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	var raw struct {
		Providers struct {
			Instances        []rawProviderAPIKey `json:"instances"`
			OpenAICompatible *rawProviderAPIKey  `json:"openaiCompatible"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("inspect provider API key sources: %w", err)
	}
	rawByName := make(map[string]string)
	for _, provider := range raw.Providers.Instances {
		if name, key := rawProviderAPIKeyValue(provider); name != "" && key != "" {
			rawByName[name] = key
		}
	}
	if legacy := raw.Providers.OpenAICompatible; legacy != nil {
		name, key := rawProviderAPIKeyValue(*legacy)
		if name == "" {
			name = "openai-compatible"
		}
		if key != "" {
			rawByName[name] = key
		}
	}

	inputs := make(map[string]ProviderAPIKeyInput, len(cfg.Providers.Instances))
	for _, provider := range cfg.Providers.Instances {
		name := strings.TrimSpace(provider.Name)
		if name == "" {
			continue
		}
		if key, ok := providerAPIKeyFromEnvironment(provider); ok {
			inputs[name] = ProviderAPIKeyInput{Source: ProviderAPIKeySourceEnvironment, APIKey: key}
			continue
		}
		if _, hasRawKey := rawByName[name]; !hasRawKey && strings.TrimSpace(provider.APIKey) != "" {
			// This fallback covers test/config callers that inject an API key
			// directly into the already-loaded Config without an env variable.
			inputs[name] = ProviderAPIKeyInput{Source: ProviderAPIKeySourceEnvironment, APIKey: strings.TrimSpace(provider.APIKey)}
		}
	}
	for name, key := range rawByName {
		input, environmentWins := inputs[name]
		if environmentWins {
			input.LegacyConfigPresent = true
			inputs[name] = input
			continue
		}
		inputs[name] = ProviderAPIKeyInput{Source: ProviderAPIKeySourceLegacyConfig, APIKey: key, LegacyConfigPresent: true}
	}
	return inputs, nil
}

// ProviderTransportSecretInput contains legacy plaintext transport values found
// in raw config.json. Callers must move these values into ProviderVault before
// rewriting the config; this type is runtime-only and is never serialized.
type ProviderTransportSecretInput struct {
	ProxyUsername               string
	ProxyPassword               string
	RequestHeaders              []ProviderRequestHeader
	LegacyProxyAuthPresent      bool
	LegacyRequestHeadersPresent bool
}

func (input ProviderTransportSecretInput) LegacyPresent() bool {
	return input.LegacyProxyAuthPresent || input.LegacyRequestHeadersPresent
}

// InspectProviderTransportSecretInputs recovers legacy proxy userinfo and
// request-header values directly from raw config.json so startup can migrate
// them into the encrypted Provider vault before the config is scrubbed.
func InspectProviderTransportSecretInputs(path string) (map[string]ProviderTransportSecretInput, error) {
	resolved, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(resolved)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(data) == 0 {
		data = []byte(`{}`)
	}
	var raw struct {
		Providers struct {
			Instances        []rawProviderTransport `json:"instances"`
			OpenAICompatible *rawProviderTransport  `json:"openaiCompatible"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("inspect provider transport secret sources: %w", err)
	}
	inputs := make(map[string]ProviderTransportSecretInput)
	for _, provider := range raw.Providers.Instances {
		if name, input := rawProviderTransportValue(provider); name != "" && input.LegacyPresent() {
			inputs[name] = input
		}
	}
	if legacy := raw.Providers.OpenAICompatible; legacy != nil {
		name, input := rawProviderTransportValue(*legacy)
		if name == "" {
			name = "openai-compatible"
		}
		if input.LegacyPresent() {
			inputs[name] = input
		}
	}
	return inputs, nil
}

type rawProviderTransport struct {
	Name           string                     `json:"name"`
	ProxyURL       string                     `json:"proxyUrl"`
	RequestHeaders []rawProviderRequestHeader `json:"requestHeaders"`
}

type rawProviderRequestHeader struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

func rawProviderTransportValue(provider rawProviderTransport) (string, ProviderTransportSecretInput) {
	name := strings.TrimSpace(provider.Name)
	input := ProviderTransportSecretInput{}
	if parsed, err := url.Parse(strings.TrimSpace(provider.ProxyURL)); err == nil && parsed.User != nil {
		input.ProxyUsername = parsed.User.Username()
		input.ProxyPassword, _ = parsed.User.Password()
		input.LegacyProxyAuthPresent = input.ProxyUsername != "" || input.ProxyPassword != ""
	}
	for _, header := range provider.RequestHeaders {
		headerName := strings.TrimSpace(header.Name)
		if headerName == "" || len(header.Value) == 0 || string(header.Value) == "null" {
			continue
		}
		var value string
		if json.Unmarshal(header.Value, &value) != nil || value == "" {
			continue
		}
		input.RequestHeaders = append(input.RequestHeaders, ProviderRequestHeader{Name: headerName, Value: value})
	}
	input.LegacyRequestHeadersPresent = len(input.RequestHeaders) > 0
	return name, input
}

type rawProviderAPIKey struct {
	Name    string          `json:"name"`
	Type    string          `json:"type"`
	Profile string          `json:"profile"`
	APIKey  json.RawMessage `json:"apiKey"`
}

func rawProviderAPIKeyValue(provider rawProviderAPIKey) (string, string) {
	name := strings.TrimSpace(provider.Name)
	if len(provider.APIKey) == 0 || string(provider.APIKey) == "null" {
		return name, ""
	}
	var value string
	if json.Unmarshal(provider.APIKey, &value) != nil {
		return name, ""
	}
	return name, strings.TrimSpace(value)
}

func providerAPIKeyFromEnvironment(provider ProviderConfig) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(provider.Name))
	typeName := strings.ToLower(strings.TrimSpace(provider.Type))
	profile := strings.ToLower(strings.TrimSpace(provider.Profile))
	var names []string
	switch {
	case profile == ProviderProfileCLIProxyAPI:
		names = []string{"CLIPROXYAPI_API_KEY", "CLIPROXY_API_KEY", "CLI_PROXY_API_KEY", "CPA_API_KEY"}
	case name == "groq":
		names = []string{"GROQ_API_KEY"}
	case typeName == ProviderTypeCodex || name == ProviderTypeCodex:
		return "", false
	case name == ProviderNameDeepSeek:
		names = []string{"DEEPSEEK_API_KEY"}
	case typeName == "anthropic" || name == "anthropic":
		names = []string{"ANTHROPIC_API_KEY"}
	case typeName == "gemini-interactions" || name == "gemini-interactions" || name == "gemini":
		names = []string{"GEMINI_API_KEY"}
	case typeName == "openai" || name == "openai":
		names = []string{"OPENAI_API_KEY"}
	case typeName == "openai-compatible" || name == "openai-compatible":
		names = []string{"OPENAI_COMPATIBLE_API_KEY", "OPENAI_API_KEY"}
	default:
		return "", false
	}
	for _, envName := range names {
		if value, ok := os.LookupEnv(envName); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}
