package pricing

import (
	"math"
	"testing"
)

func TestModelTokenPriceDoesNotDependOnProviderName(t *testing.T) {
	openAI, ok := ModelTokenPrice("custom-relay", "gpt-4.1-mini")
	if !ok || openAI.InputPerMTok != 0.40 {
		t.Fatalf("expected model catalog OpenAI price, got %+v ok=%v", openAI, ok)
	}
	anthropic, ok := ModelTokenPrice("custom-relay", "claude-sonnet-4-5")
	if !ok || anthropic.InputPerMTok != 3.00 {
		t.Fatalf("expected model catalog Anthropic price, got %+v ok=%v", anthropic, ok)
	}
	if _, ok := ModelTokenPrice("openai", "custom-model"); ok {
		t.Fatal("unknown model must not inherit provider pricing")
	}
}

func TestEstimateUsageCostUSDHandlesQualifiedModelsAndTokenBounds(t *testing.T) {
	cost := EstimateUsageCostUSD("aggregate:fast", "openai:gpt-4.1-mini", Usage{
		InputTokens:       1_000_000,
		CachedInputTokens: 250_000,
		OutputTokens:      100_000,
	})
	if math.Abs(cost-0.485) > 0.000001 {
		t.Fatalf("unexpected shared estimate: %.6f", cost)
	}
	gpt56 := EstimateUsageCostUSD("codex", "gpt-5.6-sol", Usage{InputTokens: 1_000_000, OutputTokens: 100_000})
	if math.Abs(gpt56-7.25) > 0.000001 {
		t.Fatalf("gpt-5.6 catalog models should use the coarse GPT-5.5 estimate: %.6f", gpt56)
	}
	bounded := EstimateUsageCostUSD("openai", "gpt-4.1-mini", Usage{InputTokens: 100, CachedInputTokens: 200, OutputTokens: -1})
	if math.Abs(bounded-0.00001) > 0.000000001 {
		t.Fatalf("cached/output bounds were not applied: %.9f", bounded)
	}
}
