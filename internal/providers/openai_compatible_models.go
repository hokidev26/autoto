package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// knownCodexIdentityReasoningEfforts is an exact model-id allow-list for Codex
// identities that custom OpenAI-compatible relays often serve without
// advertising supported_reasoning_levels on /models. The ceilings match the
// authenticated Codex catalog: terra serves ultra, luna and sol serve max,
// gpt-5.5 stops at xhigh. Prefix or suffix guessing is rejected because an
// unsupported level is an HTTP 400. An explicit catalog list always wins.
var knownCodexIdentityReasoningEfforts = map[string][]string{
	"gpt-5.6-sol":   {"low", "medium", "high", "xhigh", "max"},
	"gpt-5.6-luna":  {"low", "medium", "high", "xhigh", "max"},
	"gpt-5.6-terra": {"low", "medium", "high", "xhigh", "max", "ultra"},
	"gpt-5.5":       {"low", "medium", "high", "xhigh"},
}

func reasoningEffortsForKnownCodexIdentity(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	if efforts, ok := knownCodexIdentityReasoningEfforts[model]; ok {
		return append([]string(nil), efforts...)
	}
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		if efforts, ok := knownCodexIdentityReasoningEfforts[strings.TrimSpace(model[slash+1:])]; ok {
			return append([]string(nil), efforts...)
		}
	}
	return nil
}

func (p *OpenAICompatible) catalogModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	model = strings.TrimSpace(model)
	p.modelCapabilitiesMu.RLock()
	defer p.modelCapabilitiesMu.RUnlock()
	if capabilities, ok := p.modelCapabilities[model]; ok {
		return capabilities
	}
	if slash := strings.LastIndex(model, "/"); slash >= 0 {
		if capabilities, ok := p.modelCapabilities[strings.TrimSpace(model[slash+1:])]; ok {
			return capabilities
		}
	}
	return ModelCapabilities{}
}

func (p *OpenAICompatible) replaceModelCapabilities(capabilities map[string]ModelCapabilities) {
	if p == nil {
		return
	}
	if capabilities == nil {
		capabilities = make(map[string]ModelCapabilities)
	}
	p.modelCapabilitiesMu.Lock()
	p.modelCapabilities = capabilities
	p.modelCapabilitiesMu.Unlock()
}

func parseOpenAICompatibleModelCatalog(reader io.Reader) ([]string, map[string]ModelCapabilities, error) {
	data, err := io.ReadAll(io.LimitReader(reader, codexMaxResponseBytes+1))
	if err != nil || int64(len(data)) > codexMaxResponseBytes {
		return nil, nil, errors.New("models response was invalid")
	}
	var body struct {
		Data   []json.RawMessage `json:"data"`
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, nil, errors.New("models response was invalid")
	}
	seen := map[string]struct{}{}
	capabilities := make(map[string]ModelCapabilities)
	models := make([]string, 0, len(body.Data)+len(body.Models))
	appendEntry := func(raw json.RawMessage) {
		name, caps, ok := parseOpenAICompatibleModelEntry(raw)
		if !ok {
			return
		}
		if len(caps.ReasoningEfforts) > 0 {
			capabilities[name] = caps
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		models = append(models, name)
	}
	for _, raw := range body.Data {
		appendEntry(raw)
	}
	for _, raw := range body.Models {
		appendEntry(raw)
	}
	return models, capabilities, nil
}

func parseOpenAICompatibleModelEntry(raw json.RawMessage) (string, ModelCapabilities, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", ModelCapabilities{}, false
	}
	var item struct {
		ID                        string          `json:"id"`
		Slug                      string          `json:"slug"`
		SupportedReasoningLevels  json.RawMessage `json:"supported_reasoning_levels"`
		SupportedReasoningEfforts json.RawMessage `json:"supported_reasoning_efforts"`
		ReasoningEfforts          json.RawMessage `json:"reasoning_efforts"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return "", ModelCapabilities{}, false
	}
	name := strings.TrimSpace(item.Slug)
	if name == "" {
		name = strings.TrimSpace(item.ID)
	}
	if name == "" {
		return "", ModelCapabilities{}, false
	}
	efforts := canonicalReasoningEffortValues(append(
		append(
			parseCatalogReasoningEffortField(item.SupportedReasoningLevels),
			parseCatalogReasoningEffortField(item.SupportedReasoningEfforts)...,
		),
		parseCatalogReasoningEffortField(item.ReasoningEfforts)...,
	))
	return name, ModelCapabilities{ReasoningEfforts: efforts}, true
}

func parseCatalogReasoningEffortField(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err == nil {
		return names
	}
	var levels []codexReasoningLevel
	if err := json.Unmarshal(raw, &levels); err == nil {
		efforts := make([]string, 0, len(levels))
		for _, level := range levels {
			if effort := strings.TrimSpace(level.Effort); effort != "" {
				efforts = append(efforts, effort)
			}
		}
		return efforts
	}
	return nil
}
