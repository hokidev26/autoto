package providers

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The header names and the absence of reset headers were confirmed against
// xAI's Grok CLI proxy (POST https://cli-chat-proxy.grok.com/v1/responses).
func TestSubscriptionQuotaFromHeadersReadsObservedGrokHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("x-ratelimit-limit-requests", "21")
	header.Set("x-ratelimit-remaining-requests", "20")
	header.Set("x-ratelimit-limit-tokens", "1000000")
	header.Set("x-ratelimit-remaining-tokens", "999984")

	fetchedAt := time.Date(2026, 7, 25, 12, 16, 21, 0, time.UTC)
	snapshot := subscriptionQuotaFromHeaders("grok", "subauth_1", header, fetchedAt)

	if snapshot.Provider != "grok" || snapshot.AccountID != "subauth_1" {
		t.Fatalf("unexpected identity: %+v", snapshot)
	}
	if snapshot.Requests.Limit != "21" || snapshot.Requests.Remaining != "20" {
		t.Fatalf("unexpected request budget: %+v", snapshot.Requests)
	}
	if snapshot.Tokens.Limit != "1000000" || snapshot.Tokens.Remaining != "999984" {
		t.Fatalf("unexpected token budget: %+v", snapshot.Tokens)
	}
	// The proxy does not send reset headers; they must stay empty rather than
	// being synthesized into a countdown the UI would display.
	if snapshot.Requests.Reset != "" || snapshot.Tokens.Reset != "" {
		t.Fatalf("reset must stay empty when upstream omits it: %+v", snapshot)
	}
	// A combined token budget must not be reported as input/output specific.
	if snapshot.InputTokens != (AccountRateLimitSnapshot{}) || snapshot.OutputTokens != (AccountRateLimitSnapshot{}) {
		t.Fatalf("combined budget leaked into split buckets: %+v", snapshot)
	}
	if !snapshot.FetchedAt.Equal(fetchedAt) {
		t.Fatalf("unexpected fetchedAt: %s", snapshot.FetchedAt)
	}
	if !hasSubscriptionQuotaData(snapshot) {
		t.Fatal("expected snapshot to be considered populated")
	}
}

func TestSubscriptionQuotaRejectsNonNumericAndNegativeCounts(t *testing.T) {
	header := http.Header{}
	header.Set("x-ratelimit-limit-requests", "unlimited")
	header.Set("x-ratelimit-remaining-requests", "-3")
	header.Set("x-ratelimit-limit-tokens", strings.Repeat("9", maxSubscriptionQuotaValueBytes+1))
	header.Set("x-ratelimit-remaining-tokens", " 12 ")

	snapshot := subscriptionQuotaFromHeaders("grok", "subauth_1", header, time.Now())
	if snapshot.Requests.Limit != "" || snapshot.Requests.Remaining != "" {
		t.Fatalf("non-numeric and negative counts must be dropped: %+v", snapshot.Requests)
	}
	if snapshot.Tokens.Limit != "" {
		t.Fatalf("oversized value must be dropped: %+v", snapshot.Tokens)
	}
	if snapshot.Tokens.Remaining != "12" {
		t.Fatalf("surrounding whitespace should normalize: %q", snapshot.Tokens.Remaining)
	}
}

func TestSubscriptionQuotaEmptyWhenNoHeadersPresent(t *testing.T) {
	if snapshot := subscriptionQuotaFromHeaders("grok", "subauth_1", http.Header{}, time.Now()); hasSubscriptionQuotaData(snapshot) {
		t.Fatalf("expected empty snapshot to be unpopulated: %+v", snapshot)
	}
	if snapshot := subscriptionQuotaFromHeaders("grok", "subauth_1", nil, time.Now()); hasSubscriptionQuotaData(snapshot) {
		t.Fatalf("nil header must not produce data: %+v", snapshot)
	}
}

func TestSubscriptionQuotaResetSanitizesControlCharacters(t *testing.T) {
	header := http.Header{}
	header.Set("x-ratelimit-limit-requests", "5")
	header.Set("retry-after", "60")
	snapshot := subscriptionQuotaFromHeaders("grok", "subauth_1", header, time.Now())
	if snapshot.RetryAfter != "60" {
		t.Fatalf("retry-after should pass through: %q", snapshot.RetryAfter)
	}
	if got := subscriptionQuotaReset("6\x000"); got != "" {
		t.Fatalf("control characters must be rejected: %q", got)
	}
}
