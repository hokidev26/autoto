package server

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

//go:embed static/* static/modules/* static/icons/*
var staticFiles embed.FS

// uiDocumentCSPTemplate is the Content-Security-Policy for the SPA document
// (and the /app OAuth page, which carries its own stricter meta CSP on top).
// The %s is a per-response nonce authorizing only the injected local-token
// script. Directives follow what the embedded frontend actually uses:
//   - script-src 'self' + nonce: modules and app.js are same-origin; the only
//     inline script is the local-token snippet, which gets the nonce.
//   - style-src 'unsafe-inline': rendered markup relies on style="" attributes.
//   - img-src/media-src data: blob:: attachments, avatars and video previews
//     use data URLs and object URLs.
//   - connect-src ws: wss:: agent/terminal streams; explicit schemes because
//     some engines historically did not match websockets against 'self'.
//   - frame-src http: https:: the workspace preview iframe loads dev servers
//     on other ports (cross-origin), so it cannot be limited to 'self'.
const uiDocumentCSPTemplate = "default-src 'self'; script-src 'self' 'nonce-%s'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' data: blob:; connect-src 'self' ws: wss:; frame-src http: https:; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// uiAssetETags maps each embedded asset to a hash of its bytes, so the browser
// can revalidate instead of re-downloading. Built once, from content rather
// than modification time: go:embed reports a zero mtime, which is why
// http.FileServer emits no Last-Modified and every conditional request missed.
var uiAssetETags = sync.OnceValue(func() map[string]string {
	tags := make(map[string]string, 256)
	static, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return tags
	}
	_ = fs.WalkDir(static, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		data, readErr := fs.ReadFile(static, path)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		// Weak: the same asset is served both plain and gzipped, and a weak tag
		// is the correct way to say those are the same representation.
		tags[path] = `W/"` + hex.EncodeToString(sum[:8]) + `"`
		return nil
	})
	return tags
})

// uiAssetCacheControl lets the browser paint from its disk copy immediately
// and revalidate in the background. no-cache forced ~190 blocking 304s through
// a tunnel before the first module could run. max-age=0 keeps the copy stale
// so the next visit still revalidates; stale-while-revalidate is what makes
// reload feel local. Long-lived immutable caching is still unused: a ?v= on a
// JavaScript module forks it into a second instance (that is how i18n locale
// state once split). CSS may use a one-off `?c=` only to drop a CDN copy.
//
// private keeps the copy off shared caches. Cloudflare caches CSS/JS by
// extension; a shared HIT of extras.css after a restart left remote
// collaborators on the previous Stop colour. CDN-Cache-Control is the
// Cloudflare-only "do not store" switch.
const uiAssetCacheControl = "private, max-age=0, stale-while-revalidate=86400"

func setUIAssetCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", uiAssetCacheControl)
	w.Header().Set("CDN-Cache-Control", "no-store")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "no-store")
}

type bundledUIAsset struct {
	body []byte
	etag string
}

var uiStylesheetImport = regexp.MustCompile(`@import\s+url\("([^"]+)"\)`)

// bundledUIStyles concatenates styles.css @imports in cascade order. The
// source file stays a list of imports so tests can audit order; serving the
// imports verbatim made a phone wait on sixteen stylesheet round trips
// before the boot overlay could even finish painting.
var bundledUIStyles = sync.OnceValue(func() bundledUIAsset {
	entry, err := staticFiles.ReadFile("static/styles.css")
	if err != nil {
		return bundledUIAsset{}
	}
	matches := uiStylesheetImport.FindAllSubmatch(entry, -1)
	if len(matches) == 0 {
		return bundledUIAsset{}
	}
	var b bytes.Buffer
	for _, match := range matches {
		rel := string(match[1])
		if i := strings.IndexByte(rel, '?'); i >= 0 {
			rel = rel[:i]
		}
		rel = path.Clean(rel)
		if !strings.HasPrefix(rel, "styles/") || !strings.HasSuffix(rel, ".css") || strings.Contains(rel, "\\") {
			return bundledUIAsset{}
		}
		data, readErr := staticFiles.ReadFile("static/" + rel)
		if readErr != nil {
			return bundledUIAsset{}
		}
		_, _ = b.Write(data)
		if len(data) == 0 || data[len(data)-1] != '\n' {
			_ = b.WriteByte('\n')
		}
	}
	body := b.Bytes()
	if len(body) == 0 {
		return bundledUIAsset{}
	}
	sum := sha256.Sum256(body)
	return bundledUIAsset{body: body, etag: `W/"` + hex.EncodeToString(sum[:8]) + `"`}
})

func (s *Server) mountUI(r interface {
	Get(pattern string, h http.HandlerFunc)
	Handle(pattern string, h http.Handler)
}) {
	r.Get("/", s.index)
	static, _ := fs.Sub(staticFiles, "static")
	fileServer := http.StripPrefix("/ui/", http.FileServer(http.FS(static)))
	// The frontend ships ~190 modules and roughly 4.6MB of text. no-store meant
	// every one came down again on every load, which is worst on a phone.
	// Compressing them and letting the browser reuse a disk copy is the whole fix.
	compress := middleware.Compress(5)
	r.Handle("/ui/*", compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assetPath := strings.TrimPrefix(r.URL.Path, "/ui/")
		if assetPath == "styles.css" {
			if bundled := bundledUIStyles(); len(bundled.body) > 0 {
				w.Header().Set("ETag", bundled.etag)
				setUIAssetCacheHeaders(w)
				http.ServeContent(w, r, "styles.css", time.Time{}, bytes.NewReader(bundled.body))
				return
			}
		}
		if etag, ok := uiAssetETags()[assetPath]; ok {
			w.Header().Set("ETag", etag)
			setUIAssetCacheHeaders(w)
		} else {
			setNoStore(w)
		}
		fileServer.ServeHTTP(w, r)
	})))
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/ui/") {
		http.NotFound(w, r)
		return
	}
	data, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	data = injectSharePreviewMetadata(data, r)
	setNoStore(w)
	w.Header().Add("Vary", "Accept-Language")
	nonce := setUIDocumentSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if s.remoteAccessGateRequired(r) {
		// Remote sessions authenticate with their own HttpOnly cookie. Never expose
		// the process-wide canonical local token through a tunneled or exposed page.
		_, _ = w.Write(data)
		return
	}
	setLocalTokenCookie(w, r, s.localToken)
	_, _ = w.Write(injectLocalToken(data, s.localToken, nonce))
}

func injectLocalToken(data []byte, token, nonce string) []byte {
	encoded, err := json.Marshal(token)
	if err != nil {
		encoded = []byte(`""`)
	}
	snippet := `<script nonce="` + nonce + `">window.AUTOTO_LOCAL_TOKEN=` + string(encoded) + `;</script>`
	text := string(data)
	if strings.Contains(text, "</head>") {
		text = strings.Replace(text, "</head>", snippet+"\n  </head>", 1)
	} else {
		text = snippet + text
	}
	return []byte(text)
}

func setLocalTokenCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     localTokenCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
}

func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

// setUIDocumentSecurityHeaders returns the freshly generated CSP script nonce
// so the caller can stamp it onto any inline script it injects.
func setUIDocumentSecurityHeaders(w http.ResponseWriter) string {
	nonce := uiCSPNonce()
	w.Header().Set("Content-Security-Policy", fmt.Sprintf(uiDocumentCSPTemplate, nonce))
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	return nonce
}

func uiCSPNonce() string {
	buf := make([]byte, 16)
	// crypto/rand.Read is documented to never fail as of Go 1.24.
	_, _ = rand.Read(buf)
	return base64.RawStdEncoding.EncodeToString(buf)
}
