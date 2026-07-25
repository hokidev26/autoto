package server

import "strings"

// The subscription OAuth callback page is rendered by the server in the browser
// tab Google (or another provider) redirects to. It used to be hardcoded
// Simplified Chinese with lang="zh-CN", which contradicted the language the user
// had selected in Autoto. The locale is captured when the login starts so the
// page matches the app's UI language rather than the browser's Accept-Language.
type subscriptionCallbackKey string

const (
	subscriptionCallbackMethodNotAllowed subscriptionCallbackKey = "methodNotAllowed"
	subscriptionCallbackInvalidHost      subscriptionCallbackKey = "invalidHost"
	subscriptionCallbackSessionEnded     subscriptionCallbackKey = "sessionEnded"
	subscriptionCallbackStateInvalid     subscriptionCallbackKey = "stateInvalid"
	subscriptionCallbackProviderDenied   subscriptionCallbackKey = "providerDenied"
	subscriptionCallbackMissingCode      subscriptionCallbackKey = "missingCode"
	subscriptionCallbackStoreFailed      subscriptionCallbackKey = "storeFailed"
	subscriptionCallbackSuccess          subscriptionCallbackKey = "success"
	subscriptionCallbackCancelled        subscriptionCallbackKey = "cancelled"
	subscriptionCallbackExpired          subscriptionCallbackKey = "expired"
	subscriptionCallbackFailed           subscriptionCallbackKey = "failed"
)

type subscriptionCallbackCopy struct {
	// Title may contain a single {provider} placeholder.
	Title   string
	Message string
}

var subscriptionCallbackCopyByLocale = map[remoteLoginLocale]map[subscriptionCallbackKey]subscriptionCallbackCopy{
	remoteLoginLocaleChineseSimplified: {
		subscriptionCallbackMethodNotAllowed: {"请求方法无效", "OAuth 回调只接受 GET 请求。"},
		subscriptionCallbackInvalidHost:      {"回调地址无效", "本地 OAuth 回调 Host 校验失败。"},
		subscriptionCallbackSessionEnded:     {"登录会话已结束", "此 OAuth 登录会话已不再有效。"},
		subscriptionCallbackStateInvalid:     {"登录校验失败", "OAuth state 校验失败，请返回 Autoto 重新开始登录。"},
		subscriptionCallbackProviderDenied:   {"{provider} 登录失败", ""},
		subscriptionCallbackMissingCode:      {"{provider} 登录失败", "授权回调缺少 authorization code，请重新开始登录。"},
		subscriptionCallbackStoreFailed:      {"{provider} 登录失败", "凭据无法安全保存，请返回 Autoto 重试。"},
		subscriptionCallbackSuccess:          {"{provider} 登录成功", "凭据已安全保存，可以关闭此页面并返回 Autoto。"},
		subscriptionCallbackCancelled:        {"登录已取消", "此 OAuth 登录会话已取消。"},
		subscriptionCallbackExpired:          {"登录已过期", "此 OAuth 登录会话已过期，请返回 Autoto 重新开始。"},
		subscriptionCallbackFailed:           {"{provider} 登录失败", "登录未能完成，请返回 Autoto 重新开始。"},
	},
	remoteLoginLocaleChineseTraditional: {
		subscriptionCallbackMethodNotAllowed: {"請求方法無效", "OAuth 回呼只接受 GET 請求。"},
		subscriptionCallbackInvalidHost:      {"回呼位址無效", "本機 OAuth 回呼 Host 驗證失敗。"},
		subscriptionCallbackSessionEnded:     {"登入工作階段已結束", "此 OAuth 登入工作階段已不再有效。"},
		subscriptionCallbackStateInvalid:     {"登入驗證失敗", "OAuth state 驗證失敗，請返回 Autoto 重新開始登入。"},
		subscriptionCallbackProviderDenied:   {"{provider} 登入失敗", ""},
		subscriptionCallbackMissingCode:      {"{provider} 登入失敗", "授權回呼缺少 authorization code，請重新開始登入。"},
		subscriptionCallbackStoreFailed:      {"{provider} 登入失敗", "憑證無法安全保存，請返回 Autoto 重試。"},
		subscriptionCallbackSuccess:          {"{provider} 登入成功", "憑證已安全保存，可以關閉此頁面並返回 Autoto。"},
		subscriptionCallbackCancelled:        {"登入已取消", "此 OAuth 登入工作階段已取消。"},
		subscriptionCallbackExpired:          {"登入已過期", "此 OAuth 登入工作階段已過期，請返回 Autoto 重新開始。"},
		subscriptionCallbackFailed:           {"{provider} 登入失敗", "登入未能完成，請返回 Autoto 重新開始。"},
	},
	remoteLoginLocaleEnglish: {
		subscriptionCallbackMethodNotAllowed: {"Invalid request method", "The OAuth callback only accepts GET requests."},
		subscriptionCallbackInvalidHost:      {"Invalid callback address", "The local OAuth callback host check failed."},
		subscriptionCallbackSessionEnded:     {"Sign-in session ended", "This OAuth sign-in session is no longer valid."},
		subscriptionCallbackStateInvalid:     {"Sign-in verification failed", "The OAuth state check failed. Return to Autoto and start again."},
		subscriptionCallbackProviderDenied:   {"{provider} sign-in failed", ""},
		subscriptionCallbackMissingCode:      {"{provider} sign-in failed", "The callback did not include an authorization code. Start again."},
		subscriptionCallbackStoreFailed:      {"{provider} sign-in failed", "The credential could not be stored securely. Return to Autoto and retry."},
		subscriptionCallbackSuccess:          {"{provider} sign-in complete", "The credential is stored securely. You can close this page and return to Autoto."},
		subscriptionCallbackCancelled:        {"Sign-in cancelled", "This OAuth sign-in session was cancelled."},
		subscriptionCallbackExpired:          {"Sign-in expired", "This OAuth sign-in session expired. Return to Autoto and start again."},
		subscriptionCallbackFailed:           {"{provider} sign-in failed", "Sign-in did not complete. Return to Autoto and start again."},
	},
}

// subscriptionCallbackText resolves the page copy. An unknown locale falls back
// to Simplified Chinese, matching the page's historical language.
func subscriptionCallbackText(locale remoteLoginLocale, key subscriptionCallbackKey, providerLabel string) (string, string) {
	table, ok := subscriptionCallbackCopyByLocale[locale]
	if !ok {
		table = subscriptionCallbackCopyByLocale[remoteLoginLocaleChineseSimplified]
	}
	copyText, ok := table[key]
	if !ok {
		copyText = table[subscriptionCallbackFailed]
	}
	title := strings.ReplaceAll(copyText.Title, "{provider}", strings.TrimSpace(providerLabel))
	return strings.TrimSpace(title), copyText.Message
}

// subscriptionCallbackLocale validates an explicitly requested locale and falls
// back to the request's Accept-Language when it is absent or unsupported.
func subscriptionCallbackLocale(requested, acceptLanguage string) remoteLoginLocale {
	// remoteLoginLocaleForTag compares lowercase subtags ("hant", "tw"), so a
	// tag arriving as "zh-TW" must be lowered before lookup.
	if trimmed := strings.ToLower(strings.TrimSpace(requested)); trimmed != "" {
		if locale, supported := remoteLoginLocaleForTag(trimmed); supported {
			return locale
		}
	}
	return remoteLoginLocaleFromAcceptLanguage(acceptLanguage)
}
