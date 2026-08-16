package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"autoto/internal/codexauth"
)

const (
	codexQuotaBetaHeader             = "codex-1"
	codexFiveHourWindow              = 5 * time.Hour
	codexSevenDayWindow              = 7 * 24 * time.Hour
	codexFiveHourWindowSeconds       = int64(codexFiveHourWindow / time.Second)
	codexSevenDayWindowSeconds       = int64(codexSevenDayWindow / time.Second)
	codexSmallWindowMaxSeconds       = int64((6 * time.Hour) / time.Second)
	codexMaxWindowMinutes            = 365 * 24 * 60
	codexQuotaSnapshotPersistTimeout = 3 * time.Second
)

type AccountQuotaLoader interface {
	CurrentProviderAccountQuota(ctx context.Context, provider, accountID string) (json.RawMessage, time.Time, error)
}

func (p *CodexProvider) SetAccountTelemetry(telemetry AccountTelemetry) {
	if p == nil {
		return
	}
	p.telemetry = telemetry
	p.quotaTelemetry = nil
	p.quotaLoader = nil
	if quotaTelemetry, ok := telemetry.(AccountQuotaTelemetry); ok {
		p.quotaTelemetry = quotaTelemetry
	}
	if loader, ok := telemetry.(AccountQuotaLoader); ok {
		p.quotaLoader = loader
	}
}

func (p *CodexProvider) codexQuotaRequestHeaders() map[string]string {
	return map[string]string{"OpenAI-Beta": codexQuotaBetaHeader}
}

func (p *CodexProvider) recordCodexQuotaFromHeaders(ctx context.Context, accountID string, header http.Header) {
	if p == nil || p.quotaTelemetry == nil || strings.TrimSpace(accountID) == "" || ctx == nil {
		return
	}
	incoming := parseCodexRateLimitHeaders(header, p.now())
	if incoming == nil {
		return
	}
	snapshot := *incoming
	if p.quotaLoader != nil {
		raw, _, err := p.quotaLoader.CurrentProviderAccountQuota(ctx, codexauth.DefaultProviderName, accountID)
		if err == nil && len(raw) > 0 {
			var existing codexauth.QuotaSnapshot
			if json.Unmarshal(raw, &existing) == nil {
				snapshot = mergeCodexQuotaSnapshot(existing, snapshot)
			}
		}
	}
	snapshot.RateLimitResetCredits = activeCodexResetCredits(snapshot.RateLimitResetCredits, p.now())
	fetchedAt := p.now().UTC()
	if parsed, err := time.Parse(time.RFC3339Nano, snapshot.FetchedAt); err == nil {
		fetchedAt = parsed.UTC()
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexQuotaSnapshotPersistTimeout)
	defer cancel()
	_ = p.quotaTelemetry.UpdateProviderAccountQuota(recordCtx, codexauth.DefaultProviderName, accountID, snapshot, fetchedAt)
}

func parseCodexRateLimitHeaders(header http.Header, now time.Time) *codexauth.QuotaSnapshot {
	if header == nil {
		return nil
	}
	primary := parseCodexRateLimitHeaderWindow(header, "primary")
	secondary := parseCodexRateLimitHeaderWindow(header, "secondary")
	if primary == nil && secondary == nil {
		return nil
	}
	normalizeCodexHeaderWindows(primary, secondary)
	stampCodexWindowResetAt(primary, now)
	stampCodexWindowResetAt(secondary, now)
	return &codexauth.QuotaSnapshot{
		PrimaryWindow:   primary,
		SecondaryWindow: secondary,
		FetchedAt:       now.UTC().Format(time.RFC3339Nano),
	}
}

func parseCodexRateLimitHeaderWindow(header http.Header, kind string) *codexauth.RateLimitWindow {
	used, hasUsed := headerFloatValue(header, "x-codex-"+kind+"-used-percent")
	resetAfter, hasReset := headerIntValue(header, "x-codex-"+kind+"-reset-after-seconds")
	minutes, hasMinutes := headerIntValue(header, "x-codex-"+kind+"-window-minutes")
	if !hasUsed && !hasReset && !hasMinutes {
		return nil
	}
	window := &codexauth.RateLimitWindow{UsedPercent: clampPercent(used)}
	if hasReset && resetAfter > 0 {
		window.ResetAfterSeconds = resetAfter
	}
	if hasMinutes && minutes > 0 {
		if minutes > codexMaxWindowMinutes {
			minutes = codexMaxWindowMinutes
		}
		window.LimitWindowSeconds = minutes * 60
	}
	return window
}

func normalizeCodexHeaderWindows(primary, secondary *codexauth.RateLimitWindow) {
	if primary == nil && secondary == nil {
		return
	}
	if windowSeconds(primary) > 0 || windowSeconds(secondary) > 0 {
		return
	}
	// ChatGPT's live /responses headers historically send primary=7d and
	// secondary=5h when window-minutes is omitted. Stamp the durations so the
	// UI does not fall back to the opposite Autoto default.
	if primary != nil && secondary == nil {
		primary.LimitWindowSeconds = codexSevenDayWindowSeconds
		return
	}
	if secondary != nil && primary == nil {
		secondary.LimitWindowSeconds = codexFiveHourWindowSeconds
		return
	}
	primary.LimitWindowSeconds = codexSevenDayWindowSeconds
	secondary.LimitWindowSeconds = codexFiveHourWindowSeconds
}

func stampCodexWindowResetAt(window *codexauth.RateLimitWindow, now time.Time) {
	if window == nil || window.ResetAt != "" || window.ResetAfterSeconds <= 0 {
		return
	}
	window.ResetAt = now.UTC().Add(time.Duration(window.ResetAfterSeconds) * time.Second).Format(time.RFC3339)
}

func mergeCodexQuotaSnapshot(existing, incoming codexauth.QuotaSnapshot) codexauth.QuotaSnapshot {
	if incoming.PrimaryWindow != nil {
		existing.PrimaryWindow = incoming.PrimaryWindow
	}
	if incoming.SecondaryWindow != nil {
		existing.SecondaryWindow = incoming.SecondaryWindow
	}
	if incoming.FetchedAt != "" {
		existing.FetchedAt = incoming.FetchedAt
	}
	return existing
}

func CodexLocalUsageWindowStarts(quota *codexauth.QuotaSnapshot, now time.Time) (last5Hours, last7Days time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	last5Hours = now.Add(-codexFiveHourWindow)
	last7Days = now.Add(-codexSevenDayWindow)
	if quota == nil {
		return last5Hours, last7Days
	}
	fetchedAt := now
	if parsed, err := time.Parse(time.RFC3339Nano, quota.FetchedAt); err == nil {
		fetchedAt = parsed.UTC()
	} else if parsed, err := time.Parse(time.RFC3339, quota.FetchedAt); err == nil {
		fetchedAt = parsed.UTC()
	}
	fiveHour, sevenDay := classifyCodexQuotaWindows(quota)
	if start := codexUsageWindowStart(fiveHour, codexFiveHourWindow, fetchedAt, now); !start.IsZero() {
		last5Hours = start
	}
	if start := codexUsageWindowStart(sevenDay, codexSevenDayWindow, fetchedAt, now); !start.IsZero() {
		last7Days = start
	}
	return last5Hours, last7Days
}

func classifyCodexQuotaWindows(quota *codexauth.QuotaSnapshot) (fiveHour, sevenDay *codexauth.RateLimitWindow) {
	if quota == nil {
		return nil, nil
	}
	assign := func(window *codexauth.RateLimitWindow) {
		switch codexQuotaWindowKind(window) {
		case "5h":
			if fiveHour == nil {
				fiveHour = window
			}
		case "7d":
			if sevenDay == nil {
				sevenDay = window
			}
		}
	}
	assign(quota.PrimaryWindow)
	assign(quota.SecondaryWindow)
	if fiveHour == nil && quota.PrimaryWindow != nil && codexQuotaWindowKind(quota.PrimaryWindow) == "" {
		fiveHour = quota.PrimaryWindow
	}
	if sevenDay == nil && quota.SecondaryWindow != nil && codexQuotaWindowKind(quota.SecondaryWindow) == "" {
		sevenDay = quota.SecondaryWindow
	}
	return fiveHour, sevenDay
}

func codexQuotaWindowKind(window *codexauth.RateLimitWindow) string {
	seconds := windowSeconds(window)
	if seconds <= 0 {
		return ""
	}
	if seconds <= codexSmallWindowMaxSeconds {
		return "5h"
	}
	return "7d"
}

func windowSeconds(window *codexauth.RateLimitWindow) int64 {
	if window == nil {
		return 0
	}
	return window.LimitWindowSeconds
}

func codexUsageWindowStart(window *codexauth.RateLimitWindow, duration time.Duration, fetchedAt, now time.Time) time.Time {
	fallback := now.Add(-duration)
	resetAt, ok := codexWindowResetAt(window, fetchedAt)
	if !ok || !resetAt.After(now) {
		return fallback
	}
	start := resetAt.Add(-duration)
	if start.After(now) {
		return fallback
	}
	return start
}

func codexWindowResetAt(window *codexauth.RateLimitWindow, fetchedAt time.Time) (time.Time, bool) {
	if window == nil {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, window.ResetAt); err == nil {
		return parsed.UTC(), true
	}
	if parsed, err := time.Parse(time.RFC3339, window.ResetAt); err == nil {
		return parsed.UTC(), true
	}
	if window.ResetAfterSeconds <= 0 {
		return time.Time{}, false
	}
	base := fetchedAt
	if base.IsZero() {
		base = time.Now().UTC()
	}
	return base.UTC().Add(time.Duration(window.ResetAfterSeconds) * time.Second), true
}

func (p *CodexProvider) attachResetCredits(ctx context.Context, item codexauth.StoredCredential, quota *codexauth.QuotaSnapshot) {
	if p == nil || quota == nil || ctx.Err() != nil {
		return
	}
	endpoint := p.resetCreditsURL()
	if endpoint == "" {
		quota.RateLimitResetCredits = activeCodexResetCredits(quota.RateLimitResetCredits, p.now())
		return
	}
	response, _, err := p.doCredentialRequestWithHeaders(ctx, item, http.MethodGet, endpoint, nil, "", p.codexQuotaRequestHeaders())
	if err != nil {
		quota.RateLimitResetCredits = activeCodexResetCredits(quota.RateLimitResetCredits, p.now())
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		quota.RateLimitResetCredits = activeCodexResetCredits(quota.RateLimitResetCredits, p.now())
		return
	}
	credits, err := parseCodexResetCredits(response.Body)
	if err != nil {
		quota.RateLimitResetCredits = activeCodexResetCredits(quota.RateLimitResetCredits, p.now())
		return
	}
	quota.RateLimitResetCredits = activeCodexResetCredits(credits, p.now())
}

func (p *CodexProvider) ConsumeRateLimitResetCredit(ctx context.Context, id string) error {
	if p != nil && p.endpointErr != nil {
		return providerUnavailableError(codexauth.DefaultProviderName, p.endpointErr.Error())
	}
	if p == nil || p.store == nil {
		return providerUnavailableError(codexauth.DefaultProviderName, "本地凭据库未配置")
	}
	endpoint := p.resetCreditsConsumeURL()
	if endpoint == "" {
		return providerUnavailableError(p.cfg.Name, "Codex 额度重置端点不可用")
	}
	item, err := p.store.GetByID(id)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"redeem_request_id": uuid.NewString()})
	if err != nil {
		return providerUnavailableError(p.cfg.Name, "无法构造 Codex 额度重置请求")
	}
	response, _, err := p.doCredentialRequestWithHeaders(ctx, item, http.MethodPost, endpoint, payload, "application/json", p.codexQuotaRequestHeaders())
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		return errCodexQuotaUnauthorized
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Codex 额度重置失败：HTTP %d", response.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	return nil
}

func (p *CodexProvider) resetCreditsURL() string {
	return p.whamURL("rate-limit-reset-credits")
}

func (p *CodexProvider) resetCreditsConsumeURL() string {
	base := p.resetCreditsURL()
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/consume"
}

func (p *CodexProvider) whamURL(leaf string) string {
	leaf = strings.Trim(strings.TrimSpace(leaf), "/")
	if leaf == "" {
		return ""
	}
	usage := p.usageURL()
	if usage == "" {
		return ""
	}
	parsed, err := url.Parse(usage)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/wham/usage") {
		return ""
	}
	parsed.Path = strings.TrimSuffix(path, "/usage") + "/" + leaf
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func parseCodexResetCredits(reader io.Reader) (*codexauth.RateLimitResetCredits, error) {
	data, err := io.ReadAll(io.LimitReader(reader, codexMaxResponseBytes+1))
	if err != nil || len(data) > codexMaxResponseBytes {
		return nil, fmt.Errorf("invalid reset-credit response")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, fmt.Errorf("invalid reset-credit response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("invalid reset-credit response")
	}
	credits := parseResetCreditsMap(root)
	if credits == nil {
		credits = parseResetCreditsMap(flexMap(root, "rate_limit_reset_credits", "rateLimitResetCredits", "credits"))
	}
	if credits == nil {
		return &codexauth.RateLimitResetCredits{}, nil
	}
	return credits, nil
}

func parseResetCreditsMap(values map[string]any) *codexauth.RateLimitResetCredits {
	if values == nil {
		return nil
	}
	credits := &codexauth.RateLimitResetCredits{
		AvailableCount: int(flexInt64(values, "available_count", "availableCount", "count")),
	}
	for _, raw := range flexSlice(values, "credits", "items") {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		expiresAt := flexTime(item, "expires_at", "expiresAt")
		if expiresAt == "" {
			continue
		}
		credits.Credits = append(credits.Credits, codexauth.RateLimitResetCredit{ExpiresAt: expiresAt})
	}
	if credits.AvailableCount == 0 && len(credits.Credits) == 0 {
		if _, hasCount := values["available_count"]; !hasCount {
			if _, hasCount := values["availableCount"]; !hasCount {
				if _, hasCount := values["count"]; !hasCount {
					if _, hasCredits := values["credits"]; !hasCredits {
						if _, hasItems := values["items"]; !hasItems {
							return nil
						}
					}
				}
			}
		}
	}
	if credits.AvailableCount == 0 && len(credits.Credits) > 0 {
		credits.AvailableCount = len(credits.Credits)
	}
	return credits
}

func activeCodexResetCredits(credits *codexauth.RateLimitResetCredits, now time.Time) *codexauth.RateLimitResetCredits {
	if credits == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	active := make([]codexauth.RateLimitResetCredit, 0, len(credits.Credits))
	for _, credit := range credits.Credits {
		expiresAt, ok := parseFlexibleTime(credit.ExpiresAt)
		if ok && !expiresAt.After(now) {
			continue
		}
		active = append(active, credit)
	}
	count := credits.AvailableCount
	if len(credits.Credits) > 0 {
		count = len(active)
	}
	if count < 0 {
		count = 0
	}
	return &codexauth.RateLimitResetCredits{AvailableCount: count, Credits: active}
}

func parseFlexibleTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC(), true
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), true
	}
	return time.Time{}, false
}

func headerFloatValue(header http.Header, name string) (float64, bool) {
	raw := strings.TrimSpace(header.Get(name))
	if raw == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return 0, false
	}
	return parsed, true
}

func headerIntValue(header http.Header, name string) (int64, bool) {
	value, ok := headerFloatValue(header, name)
	if !ok || value <= 0 || value > math.MaxInt64 {
		return 0, ok && value == 0
	}
	return int64(value), true
}
