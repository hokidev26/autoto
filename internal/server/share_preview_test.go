package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autoto/internal/config"
)

func TestSharePreviewCopyFollowsAcceptLanguage(t *testing.T) {
	traditional := sharePreviewCopyForAcceptLanguage("zh-TW,zh-Hant;q=0.9")
	if traditional.LanguageTag != "zh-TW" || !strings.Contains(traditional.Description, "機器") || strings.Contains(traditional.Description, "机器") {
		t.Fatalf("Traditional share copy leaked Simplified Chinese: %+v", traditional)
	}
	simplified := sharePreviewCopyForAcceptLanguage("")
	if simplified.LanguageTag != "zh-CN" || !strings.Contains(simplified.Description, "机器") {
		t.Fatalf("missing Accept-Language must keep the Simplified default: %+v", simplified)
	}
	english := sharePreviewCopyForAcceptLanguage("en-US,en;q=0.8")
	if english.LanguageTag != "en" || !strings.Contains(english.Description, "coding agent that runs on your machine") {
		t.Fatalf("English share copy: %+v", english)
	}
}

func TestInjectSharePreviewMetadataRewritesTheMarker(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "autoto.example.test"
	req.TLS = nil
	markRemoteHTTPS(req)
	req.Host = "autoto.example.test"
	req.Header.Set("Accept-Language", "zh-Hant-TW")

	body := injectSharePreviewMetadata([]byte("<head>\n    <title>Autoto</title>\n    "+sharePreviewMetaMark+"\n  </head>"), req)
	html := string(body)
	if strings.Contains(html, sharePreviewMetaMark) {
		t.Fatal("the share marker must be replaced, not left in the document")
	}
	for _, fragment := range []string{
		`property="og:title" content="Autoto"`,
		`property="og:description" content="Autoto 是跑在你自己機器上的 coding agent。你給它任務，它在背景做，遇到該問的事會先問。"`,
		`property="og:image" content="https://autoto.example.test/ui/icons/autoto-512.png"`,
		`property="og:url" content="https://autoto.example.test/"`,
		`name="twitter:card" content="summary"`,
	} {
		if !strings.Contains(html, fragment) {
			t.Fatalf("missing share metadata %q in %s", fragment, html)
		}
	}
	if strings.Contains(html, "正在加载项目") || strings.Contains(html, "首页") {
		t.Fatalf("share metadata must not fall back to chrome copy: %s", html)
	}
}

func TestSharePreviewIconsBypassRemotePasswordGate(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	routes := app.Routes()

	icon := httptest.NewRecorder()
	iconReq := newTestRequest(http.MethodGet, sharePreviewImagePath, nil)
	iconReq.Host = "autoto.example.test"
	markRemoteHTTPS(iconReq)
	routes.ServeHTTP(icon, iconReq)
	if icon.Code != http.StatusOK {
		t.Fatalf("share image must be fetchable without a session, got %d: %s", icon.Code, icon.Body.String())
	}
	if contentType := icon.Header().Get("Content-Type"); !strings.Contains(contentType, "image/png") {
		t.Fatalf("share image Content-Type = %q", contentType)
	}

	module := httptest.NewRecorder()
	moduleReq := newTestRequest(http.MethodGet, "/ui/modules/dom.mjs", nil)
	moduleReq.Host = "autoto.example.test"
	markRemoteHTTPS(moduleReq)
	routes.ServeHTTP(module, moduleReq)
	if module.Code != http.StatusUnauthorized {
		t.Fatalf("other UI assets must stay behind the remote gate, got %d: %s", module.Code, module.Body.String())
	}
}

func TestRemoteLoginPageCarriesSharePreviewMetadata(t *testing.T) {
	app := New(config.Config{Security: config.SecurityConfig{AccessPassword: "secret"}}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/", nil)
	request.Host = "autoto.example.test"
	markRemoteHTTPS(request)
	request.Header.Set("Accept", "text/html")
	request.Header.Set("Accept-Language", "zh-TW")

	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected the login page, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `property="og:description" content="Autoto 是跑在你自己機器上的 coding agent。你給它任務，它在背景做，遇到該問的事會先問。"`) {
		t.Fatalf("login page missing Traditional share description: %s", body)
	}
	if !strings.Contains(body, `property="og:image" content="https://autoto.example.test/ui/icons/autoto-512.png"`) {
		t.Fatalf("login page missing absolute share image: %s", body)
	}
}

func TestIndexDocumentCarriesSharePreviewMetadata(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/", nil)
	request.Host = "localhost:16888"
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected the document, got %d", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `property="og:title" content="Autoto"`) {
		t.Fatalf("index missing og:title: %s", body)
	}
	if !strings.Contains(body, `property="og:image" content="http://localhost:16888/ui/icons/autoto-512.png"`) {
		t.Fatalf("index missing absolute share image: %s", body)
	}
	if !strings.Contains(body, `name="description" content="Autoto 是跑在你自己机器上的 coding agent。`) {
		t.Fatalf("index missing default share description: %s", body)
	}
	if strings.Contains(body, sharePreviewMetaMark) {
		t.Fatal("served index still contains the share marker")
	}
}
