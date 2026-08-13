package agent

import (
	"autoto/internal/pricing"
	"autoto/internal/providers"
)

// usageAccounting groups the shared cost table lookup with the in-memory
// context-token calibration samples that later estimates consume.
type usageAccounting struct {
	calibrator contextCalibrator
}

func (u *usageAccounting) estimateCostUSD(providerName, model string, usage providers.Usage) float64 {
	return estimateUsageCostUSD(providerName, model, usage)
}

func estimateUsageCostUSD(providerName, model string, usage providers.Usage) float64 {
	return pricing.EstimateUsageCostUSD(providerName, model, pricing.Usage{
		InputTokens:       usage.InputTokens,
		OutputTokens:      usage.OutputTokens,
		CachedInputTokens: usage.CachedInputTokens,
		ReasoningTokens:   usage.ReasoningTokens,
	})
}
