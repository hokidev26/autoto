package server

import (
	"strings"
	"testing"
)

func TestSubscriptionCallbackLocalePrefersExplicitTagOverAcceptLanguage(t *testing.T) {
	// The UI language wins: a Traditional Chinese UI must not be rendered in
	// Simplified just because the redirected browser advertises zh-CN.
	if got := subscriptionCallbackLocale("zh-TW", "zh-CN,zh;q=0.9"); got != remoteLoginLocaleChineseTraditional {
		t.Fatalf("explicit zh-TW should win: got %q", got)
	}
	// Mixed case must still resolve; remoteLoginLocaleForTag compares lowercase.
	if got := subscriptionCallbackLocale("ZH-Hant", ""); got != remoteLoginLocaleChineseTraditional {
		t.Fatalf("zh-Hant should resolve to Traditional: got %q", got)
	}
	if got := subscriptionCallbackLocale("en-US", "zh-CN"); got != remoteLoginLocaleEnglish {
		t.Fatalf("explicit en should win: got %q", got)
	}
}

func TestSubscriptionCallbackLocaleFallsBackToAcceptLanguage(t *testing.T) {
	if got := subscriptionCallbackLocale("", "zh-TW,zh;q=0.9"); got != remoteLoginLocaleChineseTraditional {
		t.Fatalf("absent tag should fall back to Accept-Language: got %q", got)
	}
	// An unsupported or hostile tag must not win over the header.
	if got := subscriptionCallbackLocale("klingon", "en-US"); got != remoteLoginLocaleEnglish {
		t.Fatalf("unsupported tag should fall back: got %q", got)
	}
	// Neither signal present keeps the page's historical language.
	if got := subscriptionCallbackLocale("", ""); got != remoteLoginLocaleChineseSimplified {
		t.Fatalf("default should stay Simplified: got %q", got)
	}
}

func TestSubscriptionCallbackTextTranslatesAndSubstitutesProvider(t *testing.T) {
	title, message := subscriptionCallbackText(remoteLoginLocaleChineseTraditional, subscriptionCallbackSuccess, "Antigravity")
	if !strings.Contains(title, "Antigravity") || !strings.Contains(title, "登入成功") {
		t.Fatalf("unexpected Traditional title: %q", title)
	}
	if strings.Contains(message, "凭据") {
		t.Fatalf("Traditional message must not contain Simplified text: %q", message)
	}

	enTitle, enMessage := subscriptionCallbackText(remoteLoginLocaleEnglish, subscriptionCallbackSuccess, "Grok")
	if enTitle != "Grok sign-in complete" || !strings.Contains(enMessage, "stored securely") {
		t.Fatalf("unexpected English copy: %q / %q", enTitle, enMessage)
	}

	// Every locale must define every key, or a user would see an empty page.
	for locale, table := range subscriptionCallbackCopyByLocale {
		for _, key := range []subscriptionCallbackKey{
			subscriptionCallbackMethodNotAllowed, subscriptionCallbackInvalidHost,
			subscriptionCallbackSessionEnded, subscriptionCallbackStateInvalid,
			subscriptionCallbackProviderDenied, subscriptionCallbackMissingCode,
			subscriptionCallbackStoreFailed, subscriptionCallbackSuccess,
			subscriptionCallbackCancelled, subscriptionCallbackExpired, subscriptionCallbackFailed,
		} {
			entry, ok := table[key]
			if !ok || strings.TrimSpace(entry.Title) == "" {
				t.Fatalf("locale %q is missing a title for %q", locale, key)
			}
			// providerDenied carries the upstream message instead of a canned one.
			if key != subscriptionCallbackProviderDenied && strings.TrimSpace(entry.Message) == "" {
				t.Fatalf("locale %q is missing a message for %q", locale, key)
			}
		}
	}
}

func TestSubscriptionCallbackTextFallsBackForUnknownLocale(t *testing.T) {
	title, message := subscriptionCallbackText(remoteLoginLocale("xx"), subscriptionCallbackSuccess, "Kimi")
	if !strings.Contains(title, "Kimi") || strings.TrimSpace(message) == "" {
		t.Fatalf("unknown locale should fall back to a populated entry: %q / %q", title, message)
	}
}

func TestSubscriptionProviderLabelUsesPlatformName(t *testing.T) {
	// The gemini provider talks to Google's Antigravity surface, and the console
	// labels it that way; the callback page must agree.
	if got := subscriptionProviderLabel("gemini"); got != "Antigravity" {
		t.Fatalf("unexpected gemini label: %q", got)
	}
}
