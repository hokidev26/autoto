package server

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

const (
	sharePreviewImagePath = "/ui/icons/autoto-512.png"
	sharePreviewMetaMark  = "<!--autoto-share-meta-->"
)

// publicShareAssetPath is the unauthenticated brand artwork chat apps fetch
// when building a link preview. It is only the product icon and favicon: the
// rest of /ui/ stays behind the remote gate when no session exists.
func publicShareAssetPath(path string) bool {
	switch path {
	case sharePreviewImagePath, "/ui/icons/autoto-192.png", "/ui/favicon.ico":
		return true
	default:
		return false
	}
}

type sharePreviewCopy struct {
	LanguageTag string
	Title       string
	Description string
}

func sharePreviewCopyForAcceptLanguage(header string) sharePreviewCopy {
	switch remoteLoginLocaleFromAcceptLanguage(header) {
	case remoteLoginLocaleChineseTraditional:
		return sharePreviewCopy{
			LanguageTag: "zh-TW",
			Title:       "Autoto",
			Description: "Autoto 是跑在你自己機器上的 coding agent。你給它任務，它在背景做，遇到該問的事會先問。",
		}
	case remoteLoginLocaleEnglish:
		return sharePreviewCopy{
			LanguageTag: "en",
			Title:       "Autoto",
			Description: "Autoto is a coding agent that runs on your machine. Give it a task; it works in the background and asks before anything risky.",
		}
	default:
		return sharePreviewCopy{
			LanguageTag: "zh-CN",
			Title:       "Autoto",
			Description: "Autoto 是跑在你自己机器上的 coding agent。你给它任务，它在后台做，遇到该问的事会先问。",
		}
	}
}

func publicRequestOrigin(r *http.Request) string {
	if r == nil {
		return ""
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + host
}

func sharePreviewOGLocale(languageTag string) string {
	switch languageTag {
	case "zh-TW":
		return "zh_TW"
	case "en":
		return "en_US"
	default:
		return "zh_CN"
	}
}

func sharePreviewMetaHTML(r *http.Request) string {
	header := ""
	if r != nil {
		header = r.Header.Get("Accept-Language")
	}
	copy := sharePreviewCopyForAcceptLanguage(header)
	origin := publicRequestOrigin(r)
	image := sharePreviewImagePath
	pageURL := "/"
	if origin != "" {
		image = origin + sharePreviewImagePath
		pageURL = origin + "/"
	}
	title := html.EscapeString(copy.Title)
	description := html.EscapeString(copy.Description)
	imageURL := html.EscapeString(image)
	canon := html.EscapeString(pageURL)
	locale := html.EscapeString(sharePreviewOGLocale(copy.LanguageTag))
	return fmt.Sprintf(`<meta name="description" content="%s" />
    <meta property="og:type" content="website" />
    <meta property="og:site_name" content="Autoto" />
    <meta property="og:locale" content="%s" />
    <meta property="og:title" content="%s" />
    <meta property="og:description" content="%s" />
    <meta property="og:url" content="%s" />
    <meta property="og:image" content="%s" />
    <meta property="og:image:alt" content="Autoto" />
    <meta property="og:image:type" content="image/png" />
    <meta property="og:image:width" content="512" />
    <meta property="og:image:height" content="512" />
    <meta name="twitter:card" content="summary" />
    <meta name="twitter:title" content="%s" />
    <meta name="twitter:description" content="%s" />
    <meta name="twitter:image" content="%s" />`, description, locale, title, description, canon, imageURL, title, description, imageURL)
}

func injectSharePreviewMetadata(data []byte, r *http.Request) []byte {
	block := sharePreviewMetaHTML(r)
	text := string(data)
	if strings.Contains(text, sharePreviewMetaMark) {
		return []byte(strings.Replace(text, sharePreviewMetaMark, block, 1))
	}
	if strings.Contains(text, "</head>") {
		return []byte(strings.Replace(text, "</head>", block+"\n  </head>", 1))
	}
	return append([]byte(block), data...)
}
