package pricing

import "strings"

type Usage struct {
	InputTokens       int64
	OutputTokens      int64
	CachedInputTokens int64
	ReasoningTokens   int64
}

type TokenPrice struct {
	InputPerMTok       float64
	CachedInputPerMTok float64
	OutputPerMTok      float64
}

func EstimateUsageCostUSD(providerName, model string, usage Usage) float64 {
	price, ok := ModelTokenPrice(providerName, model)
	if !ok {
		return 0
	}
	inputTokens := maxInt64(usage.InputTokens, 0)
	cachedInput := maxInt64(usage.CachedInputTokens, 0)
	if cachedInput > inputTokens {
		cachedInput = inputTokens
	}
	uncachedInput := inputTokens - cachedInput
	outputTokens := maxInt64(usage.OutputTokens, 0)
	return (float64(uncachedInput)*price.InputPerMTok + float64(cachedInput)*price.CachedInputPerMTok + float64(outputTokens)*price.OutputPerMTok) / 1_000_000
}

// ModelTokenPrice returns coarse USD-per-million-token estimates used for local
// usage summaries. Provider names are accepted for API stability, but matching
// intentionally follows the concrete model so relays and aggregates can share it.
func ModelTokenPrice(_ string, model string) (TokenPrice, bool) {
	name := normalizedModelName(model)
	openAIPrices := []struct {
		match string
		price TokenPrice
	}{
		{match: "gpt-5.6", price: TokenPrice{InputPerMTok: 5.00, CachedInputPerMTok: 0.50, OutputPerMTok: 22.50}}, // coarse ChatGPT-catalog estimate; not an official invoice rate
		{match: "gpt-5.5", price: TokenPrice{InputPerMTok: 5.00, CachedInputPerMTok: 0.50, OutputPerMTok: 22.50}},
		{match: "gpt-5.4-mini", price: TokenPrice{InputPerMTok: 0.375, CachedInputPerMTok: 0.0375, OutputPerMTok: 2.25}},
		{match: "gpt-5.4", price: TokenPrice{InputPerMTok: 2.50, CachedInputPerMTok: 0.25, OutputPerMTok: 11.25}},
		{match: "gpt-4.1-mini", price: TokenPrice{InputPerMTok: 0.40, CachedInputPerMTok: 0.10, OutputPerMTok: 1.60}},
		{match: "gpt-4.1-nano", price: TokenPrice{InputPerMTok: 0.10, CachedInputPerMTok: 0.025, OutputPerMTok: 0.40}},
		{match: "gpt-4.1", price: TokenPrice{InputPerMTok: 2.00, CachedInputPerMTok: 0.50, OutputPerMTok: 8.00}},
		{match: "gpt-4o-mini", price: TokenPrice{InputPerMTok: 0.15, CachedInputPerMTok: 0.075, OutputPerMTok: 0.60}},
		{match: "gpt-4o", price: TokenPrice{InputPerMTok: 2.50, CachedInputPerMTok: 1.25, OutputPerMTok: 10.00}},
	}
	for _, candidate := range openAIPrices {
		if strings.HasPrefix(name, candidate.match) {
			return candidate.price, true
		}
	}

	var anthropicPrice TokenPrice
	switch {
	case strings.HasPrefix(name, "claude-sonnet-5"):
		anthropicPrice = TokenPrice{InputPerMTok: 2.00, CachedInputPerMTok: 0.20, OutputPerMTok: 10.00}
	case name == "claude-opus-4" || strings.HasPrefix(name, "claude-opus-4-1") || strings.HasPrefix(name, "claude-opus-4-202"):
		anthropicPrice = TokenPrice{InputPerMTok: 15.00, CachedInputPerMTok: 1.50, OutputPerMTok: 75.00}
	case strings.HasPrefix(name, "claude-opus-4"):
		anthropicPrice = TokenPrice{InputPerMTok: 5.00, CachedInputPerMTok: 0.50, OutputPerMTok: 25.00}
	case strings.HasPrefix(name, "claude-sonnet-4"):
		anthropicPrice = TokenPrice{InputPerMTok: 3.00, CachedInputPerMTok: 0.30, OutputPerMTok: 15.00}
	case strings.HasPrefix(name, "claude-haiku-4"):
		anthropicPrice = TokenPrice{InputPerMTok: 1.00, CachedInputPerMTok: 0.10, OutputPerMTok: 5.00}
	case strings.HasPrefix(name, "claude-3-5-haiku"):
		anthropicPrice = TokenPrice{InputPerMTok: 0.80, CachedInputPerMTok: 0.08, OutputPerMTok: 4.00}
	case strings.HasPrefix(name, "claude-3-5-sonnet") || strings.HasPrefix(name, "claude-3-7-sonnet"):
		anthropicPrice = TokenPrice{InputPerMTok: 3.00, CachedInputPerMTok: 0.30, OutputPerMTok: 15.00}
	}
	if anthropicPrice != (TokenPrice{}) {
		return anthropicPrice, true
	}
	return TokenPrice{}, false
}

func normalizedModelName(model string) string {
	name := strings.ToLower(strings.TrimSpace(model))
	if separator := strings.IndexByte(name, ':'); separator >= 0 && separator+1 < len(name) {
		prefix := strings.TrimSpace(name[:separator])
		suffix := strings.TrimSpace(name[separator+1:])
		if prefix != "" && suffix != "" {
			return suffix
		}
	}
	return name
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}
