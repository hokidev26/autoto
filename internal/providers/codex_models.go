package providers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"

	"autoto/internal/codexauth"
)

// Codex model catalog parsing and capability derivation. Split out of
// codex.go to keep that file inside the source size budget.

type codexReasoningLevel struct {
	Effort string `json:"effort"`
}

type codexModelCatalogEntry struct {
	Slug string `json:"slug"`
	ID   string `json:"id"`
	// SupportedReasoningLevels is the authoritative per-model effort list. The
	// levels genuinely differ — gpt-5.6-terra serves "ultra", gpt-5.6-luna stops
	// at "max", gpt-5.5 stops at "xhigh" — and asking for one a model does not
	// serve is answered with HTTP 400, so this cannot be inferred from the
	// provider or guessed from the model name.
	SupportedReasoningLevels []codexReasoningLevel `json:"supported_reasoning_levels"`
	FastMode                 *bool                 `json:"fast_mode"`
	SupportsFastMode         *bool                 `json:"supports_fast_mode"`
	ServiceTier              json.RawMessage       `json:"service_tier"`
	ServiceTiers             json.RawMessage       `json:"service_tiers"`
	SupportedServiceTiers    json.RawMessage       `json:"supported_service_tiers"`
	AdditionalSpeedTiers     json.RawMessage       `json:"additional_speed_tiers"`
	SupportedSpeedTiers      json.RawMessage       `json:"supported_speed_tiers"`
}

func parseCodexModels(reader io.Reader) ([]string, error) {
	models, _, err := parseCodexModelCatalog(reader)
	return models, err
}

func parseCodexModelCatalog(reader io.Reader) ([]string, map[string]ModelCapabilities, error) {
	var body struct {
		Models []codexModelCatalogEntry `json:"models"`
		Data   []codexModelCatalogEntry `json:"data"`
	}
	if err := decodeLimitedJSON(reader, codexMaxResponseBytes, &body); err != nil {
		return nil, nil, errors.New("Codex 模型列表响应无效")
	}
	seen := map[string]struct{}{}
	capabilities := make(map[string]ModelCapabilities)
	models := make([]string, 0, len(body.Models)+len(body.Data))
	appendEntry := func(item codexModelCatalogEntry) {
		model := strings.TrimSpace(item.Slug)
		if model == "" {
			model = strings.TrimSpace(item.ID)
		}
		if model == "" {
			return
		}
		if supportsFastMode, known := codexModelEntryFastModeCapability(item); known {
			current := capabilities[model]
			current.FastMode = current.FastMode || supportsFastMode
			current.FastModeKnown = true
			capabilities[model] = current
		}
		if efforts := codexModelEntryReasoningEfforts(item); len(efforts) > 0 {
			current := capabilities[model]
			current.ReasoningEfforts = efforts
			capabilities[model] = current
		}
		if _, exists := seen[model]; exists {
			return
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	for _, item := range body.Models {
		appendEntry(item)
	}
	for _, item := range body.Data {
		appendEntry(item)
	}
	sort.Strings(models)
	return models, capabilities, nil
}

// codexModelEntryReasoningEfforts keeps only levels Autoto has a control for.
// The catalog may name a level this build does not model yet; offering it would
// produce a picker entry nothing downstream can validate or persist.
func codexModelEntryReasoningEfforts(item codexModelCatalogEntry) []string {
	efforts := make([]string, 0, len(item.SupportedReasoningLevels))
	for _, level := range item.SupportedReasoningLevels {
		if effort := strings.ToLower(strings.TrimSpace(level.Effort)); effort != "" {
			efforts = append(efforts, effort)
		}
	}
	return canonicalReasoningEffortValues(efforts)
}

func codexModelEntryFastModeCapability(item codexModelCatalogEntry) (bool, bool) {
	supported := false
	known := false
	for _, value := range []*bool{item.FastMode, item.SupportsFastMode} {
		if value == nil {
			continue
		}
		known = true
		supported = supported || *value
	}
	for _, value := range []json.RawMessage{
		item.ServiceTier,
		item.ServiceTiers,
		item.SupportedServiceTiers,
		item.AdditionalSpeedTiers,
		item.SupportedSpeedTiers,
	} {
		fieldSupportsFastMode, fieldKnown := codexSpeedTierCapability(value)
		known = known || fieldKnown
		supported = supported || fieldSupportsFastMode
	}
	return supported, known
}

func codexSpeedTierCapability(value json.RawMessage) (bool, bool) {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || bytes.Equal(value, []byte("null")) {
		return false, false
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return false, false
	}
	return codexSpeedTierSupportsFastMode(decoded), true
}

func codexSpeedTierSupportsFastMode(value any) bool {
	switch typed := value.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "fast", "priority":
			return true
		default:
			return false
		}
	case []any:
		for _, item := range typed {
			if codexSpeedTierSupportsFastMode(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range typed {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if normalizedKey == "fast_mode" || normalizedKey == "supports_fast_mode" {
				if enabled, ok := item.(bool); ok && enabled {
					return true
				}
			}
			switch normalizedKey {
			case "service_tier", "service_tiers", "tier", "name", "id", "slug", "value", "speed", "mode", "additional_speed_tiers", "supported_speed_tiers":
				if codexSpeedTierSupportsFastMode(item) {
					return true
				}
			}
		}
	}
	return false
}

func decodeLimitedJSON(reader io.Reader, limit int64, dst any) error {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit {
		return errors.New("response too large")
	}
	return json.Unmarshal(data, dst)
}

func supplementCodexModelCapabilities(capabilities map[string]ModelCapabilities, models []string, baseURL string) map[string]ModelCapabilities {
	if strings.TrimRight(strings.TrimSpace(baseURL), "/") != codexauth.DefaultBaseURL {
		return capabilities
	}
	for _, rawModel := range models {
		model := strings.TrimSpace(rawModel)
		knownEfforts, known := codexKnownOfficialModelReasoningEfforts[model]
		if !known || len(knownEfforts) == 0 {
			continue
		}
		current := capabilities[model]
		// The authenticated catalog is authoritative. Only fill an omitted list;
		// never append max to an explicit list that intentionally stops at xhigh.
		if len(current.ReasoningEfforts) > 0 {
			continue
		}
		current.ReasoningEfforts = append([]string(nil), knownEfforts...)
		if capabilities == nil {
			capabilities = make(map[string]ModelCapabilities)
		}
		capabilities[model] = current
	}
	return capabilities
}

func fallbackCodexModelCapabilities(baseURL string) map[string]ModelCapabilities {
	if strings.TrimRight(strings.TrimSpace(baseURL), "/") != codexauth.DefaultBaseURL {
		return make(map[string]ModelCapabilities)
	}
	models, capabilities, err := parseCodexModelCatalog(strings.NewReader(codexFallbackFastModelCatalog))
	if err != nil {
		return make(map[string]ModelCapabilities)
	}
	return supplementCodexModelCapabilities(capabilities, models, baseURL)
}

func fallbackCodexModels(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = codexauth.DefaultModel
	}
	return []string{model}
}
