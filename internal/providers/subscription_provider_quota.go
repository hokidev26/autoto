package providers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Subscription CLI proxies advertise remaining quota through OpenAI-style
// rate-limit response headers. Observed on xAI's Grok proxy (/v1/responses):
//
//	x-ratelimit-limit-requests / x-ratelimit-remaining-requests
//	x-ratelimit-limit-tokens   / x-ratelimit-remaining-tokens
//
// The proxy does not send the x-ratelimit-reset-* headers that xAI's public API
// documents, so Reset stays empty instead of being guessed. Tokens land in the
// combined bucket because the upstream reports one budget, not input/output.
const (
	subscriptionRateLimitRequestsLimit     = "x-ratelimit-limit-requests"
	subscriptionRateLimitRequestsRemaining = "x-ratelimit-remaining-requests"
	subscriptionRateLimitRequestsReset     = "x-ratelimit-reset-requests"
	subscriptionRateLimitTokensLimit       = "x-ratelimit-limit-tokens"
	subscriptionRateLimitTokensRemaining   = "x-ratelimit-remaining-tokens"
	subscriptionRateLimitTokensReset       = "x-ratelimit-reset-tokens"
)

// maxSubscriptionQuotaValueBytes bounds how much of an upstream header we are
// willing to echo back to the account UI.
const maxSubscriptionQuotaValueBytes = 40

func subscriptionQuotaFromHeaders(provider, accountID string, header http.Header, fetchedAt time.Time) ProviderAccountQuotaSnapshot {
	if header == nil {
		return ProviderAccountQuotaSnapshot{}
	}
	return ProviderAccountQuotaSnapshot{
		Provider:  provider,
		AccountID: accountID,
		Requests: AccountRateLimitSnapshot{
			Limit:     subscriptionQuotaCount(header.Get(subscriptionRateLimitRequestsLimit)),
			Remaining: subscriptionQuotaCount(header.Get(subscriptionRateLimitRequestsRemaining)),
			Reset:     subscriptionQuotaReset(header.Get(subscriptionRateLimitRequestsReset)),
		},
		Tokens: AccountRateLimitSnapshot{
			Limit:     subscriptionQuotaCount(header.Get(subscriptionRateLimitTokensLimit)),
			Remaining: subscriptionQuotaCount(header.Get(subscriptionRateLimitTokensRemaining)),
			Reset:     subscriptionQuotaReset(header.Get(subscriptionRateLimitTokensReset)),
		},
		RetryAfter: subscriptionQuotaReset(header.Get("retry-after")),
		FetchedAt:  fetchedAt,
	}
}

// subscriptionQuotaCount keeps only non-negative integer counts. Anything else
// is dropped so the UI never renders an upstream string as a quota number.
func subscriptionQuotaCount(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxSubscriptionQuotaValueBytes {
		return ""
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed < 0 {
		return ""
	}
	return strconv.FormatInt(parsed, 10)
}

// subscriptionQuotaReset passes through a bounded, printable reset hint. The
// upstreams use both durations ("60s") and seconds ("60"), so the value stays a
// string and is only sanitized.
func subscriptionQuotaReset(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxSubscriptionQuotaValueBytes {
		return ""
	}
	for _, char := range trimmed {
		if char < 0x20 || char == 0x7f {
			return ""
		}
	}
	return trimmed
}

func hasSubscriptionQuotaData(snapshot ProviderAccountQuotaSnapshot) bool {
	for _, limit := range []AccountRateLimitSnapshot{snapshot.Requests, snapshot.Tokens} {
		if limit.Limit != "" || limit.Remaining != "" || limit.Reset != "" {
			return true
		}
	}
	return snapshot.RetryAfter != ""
}

// recordAccountQuota persists a quota snapshot when the injected telemetry also
// implements AccountQuotaTelemetry. It mirrors recordAttempt: never blocking the
// request path, never failing it.
func (a *subscriptionProviderAccounts) recordAccountQuota(ctx context.Context, accountID string, header http.Header) {
	if a == nil || ctx == nil || ctx.Err() != nil || strings.TrimSpace(accountID) == "" {
		return
	}
	a.telemetryMu.RLock()
	telemetry := a.telemetry
	a.telemetryMu.RUnlock()
	quotaTelemetry, ok := telemetry.(AccountQuotaTelemetry)
	if !ok || quotaTelemetry == nil {
		return
	}
	snapshot := subscriptionQuotaFromHeaders(a.providerName(), accountID, header, a.now().UTC())
	if !hasSubscriptionQuotaData(snapshot) {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = quotaTelemetry.UpdateProviderAccountQuota(recordCtx, snapshot.Provider, snapshot.AccountID, snapshot, snapshot.FetchedAt)
}
