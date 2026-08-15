package server

import (
	"net/http"
	"runtime/debug"
	"sort"
)

type licenseModule struct {
	Path     string `json:"path"`
	Version  string `json:"version,omitempty"`
	License  string `json:"license"`
	Relation string `json:"relation"`
}

// knownLicenses is deliberately an allowlist rather than a guess based on the
// module name. The entries below were checked against the corresponding module
// LICENSE/COPYING file (and the module's go.mod when the license is declared
// there) for the versions selected by go.mod. Keep this table reviewed when
// changing dependencies; an unreviewed module must remain "unknown".
var knownLicenses = map[string]string{
	"github.com/adrg/xdg":                                      "MIT",
	"github.com/anthropics/anthropic-sdk-go":                   "MIT",
	"github.com/bahlo/generic-list-go":                         "BSD-3-Clause",
	"github.com/buger/jsonparser":                              "MIT",
	"github.com/coder/websocket":                               "ISC",
	"github.com/coreos/go-oidc/v3":                             "Apache-2.0",
	"github.com/creack/pty":                                    "MIT",
	"github.com/dustin/go-humanize":                            "MIT",
	"github.com/go-chi/chi/v5":                                 "MIT",
	"github.com/go-jose/go-jose/v4":                            "Apache-2.0",
	"github.com/go-ole/go-ole":                                 "MIT",
	"github.com/godbus/dbus/v5":                                "BSD-3-Clause",
	"github.com/google/uuid":                                   "BSD-3-Clause",
	"github.com/invopop/jsonschema":                            "MIT",
	"github.com/jchv/go-winloader":                             "ISC",
	"github.com/mattn/go-colorable":                            "MIT",
	"github.com/mattn/go-isatty":                               "MIT",
	"github.com/ncruces/go-strftime":                           "MIT",
	"github.com/openai/openai-go/v3":                           "Apache-2.0",
	"github.com/pb33f/ordered-map/v2":                          "Apache-2.0",
	"github.com/remyoudompheng/bigfft":                         "BSD-3-Clause",
	"github.com/standard-webhooks/standard-webhooks/libraries": "MIT",
	"github.com/tidwall/gjson":                                 "MIT",
	"github.com/tidwall/match":                                 "MIT",
	"github.com/tidwall/pretty":                                "MIT",
	"github.com/tidwall/sjson":                                 "MIT",
	"github.com/wailsapp/wails/v3":                             "MIT",
	"go.yaml.in/yaml/v4":                                       "MIT OR Apache-2.0",
	"golang.org/x/crypto":                                      "BSD-3-Clause",
	"golang.org/x/exp":                                         "BSD-3-Clause",
	"golang.org/x/oauth2":                                      "BSD-3-Clause",
	"golang.org/x/sync":                                        "BSD-3-Clause",
	"golang.org/x/sys":                                         "BSD-3-Clause",
	"golang.org/x/text":                                        "BSD-3-Clause",
	"gopkg.in/yaml.v3":                                         "MIT",
	"modernc.org/cc/v4":                                        "BSD-3-Clause",
	"modernc.org/ccgo/v4":                                      "BSD-3-Clause",
	"modernc.org/fileutil":                                     "BSD-3-Clause",
	"modernc.org/gc/v2":                                        "BSD-3-Clause",
	"modernc.org/gc/v3":                                        "BSD-3-Clause",
	"modernc.org/goabi0":                                       "BSD-3-Clause",
	"modernc.org/libc":                                         "BSD-3-Clause",
	"modernc.org/mathutil":                                     "BSD-3-Clause",
	"modernc.org/memory":                                       "BSD-3-Clause",
	"modernc.org/opt":                                          "BSD-3-Clause",
	"modernc.org/sortutil":                                     "BSD-3-Clause",
	"modernc.org/sqlite":                                       "BSD-3-Clause",
	"modernc.org/strutil":                                      "BSD-3-Clause",
	"modernc.org/token":                                        "BSD-3-Clause",
	"mvdan.cc/sh/v3":                                           "BSD-3-Clause",
}

// Keep this in sync with the first require block in go.mod. The old list only
// contained seven modules, so every other direct module was mislabeled as
// indirect (or was absent when debug.ReadBuildInfo was unavailable).
var directModules = map[string]struct{}{
	"github.com/anthropics/anthropic-sdk-go": {},
	"github.com/coder/websocket":             {},
	"github.com/coreos/go-oidc/v3":           {},
	"github.com/creack/pty":                  {},
	"github.com/go-chi/chi/v5":               {},
	"github.com/google/uuid":                 {},
	"github.com/openai/openai-go/v3":         {},
	"github.com/wailsapp/wails/v3":           {},
	"golang.org/x/crypto":                    {},
	"golang.org/x/oauth2":                    {},
	"golang.org/x/sync":                      {},
	"golang.org/x/sys":                       {},
	"golang.org/x/text":                      {},
	"gopkg.in/yaml.v3":                       {},
	"modernc.org/sqlite":                     {},
	"mvdan.cc/sh/v3":                         {},
}

func (s *Server) licenses(w http.ResponseWriter, r *http.Request) {
	info, ok := debug.ReadBuildInfo()
	modulesByPath := map[string]licenseModule{}
	if ok {
		for _, dep := range info.Deps {
			modulesByPath[dep.Path] = licenseModule{Path: dep.Path, Version: dep.Version, License: licenseFor(dep.Path), Relation: relationFor(dep.Path)}
		}
	}
	for path := range directModules {
		if _, ok := modulesByPath[path]; !ok {
			modulesByPath[path] = licenseModule{Path: path, License: licenseFor(path), Relation: "direct"}
		}
	}
	modules := make([]licenseModule, 0, len(modulesByPath))
	for _, module := range modulesByPath {
		modules = append(modules, module)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	writeJSON(w, http.StatusOK, map[string]any{
		"notice":  "Development aid only; verify before distribution. Not legal advice.",
		"modules": modules,
	})
}

func licenseFor(path string) string {
	if license, ok := knownLicenses[path]; ok {
		return license
	}
	return "unknown"
}

func relationFor(path string) string {
	if _, ok := directModules[path]; ok {
		return "direct"
	}
	return "indirect"
}
