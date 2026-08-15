package server

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLicenseForReviewedModules(t *testing.T) {
	for path, want := range knownLicenses {
		if got := licenseFor(path); got != want {
			t.Errorf("licenseFor(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestLicenseForUnknownDoesNotGuess(t *testing.T) {
	if got := licenseFor("example.invalid/unreviewed"); got != "unknown" {
		t.Fatalf("licenseFor(unreviewed) = %q, want unknown", got)
	}
}

func TestDirectModulesMatchGoModDirectRequirements(t *testing.T) {
	want := parseGoModModules(t).direct
	if len(directModules) != len(want) {
		t.Fatalf("direct module count = %d, want %d from go.mod", len(directModules), len(want))
	}
	for path := range want {
		if _, ok := directModules[path]; !ok {
			t.Errorf("direct module %q is in go.mod but missing from directModules", path)
		}
	}
	for path := range directModules {
		if _, ok := want[path]; !ok {
			t.Errorf("directModules extra %q is not a go.mod direct requirement", path)
		}
	}
}

func TestKnownLicensesCoverGoModModules(t *testing.T) {
	for path := range parseGoModModules(t).all {
		if licenseFor(path) == "unknown" {
			t.Errorf("%s is in go.mod but knownLicenses has no reviewed entry", path)
		}
	}
}

func TestRelationForDirectAndIndirect(t *testing.T) {
	if got := relationFor("github.com/coreos/go-oidc/v3"); got != "direct" {
		t.Fatalf("direct relation = %q, want direct", got)
	}
	if got := relationFor("github.com/adrg/xdg"); got != "indirect" {
		t.Fatalf("indirect relation = %q, want indirect", got)
	}
}

type goModModules struct {
	direct map[string]struct{}
	all    map[string]struct{}
}

func parseGoModModules(t *testing.T) goModModules {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	direct := map[string]struct{}{}
	all := map[string]struct{}{}
	inRequire := false
	block := 0
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "require (") {
			inRequire = true
			continue
		}
		if strings.HasPrefix(trimmed, "require ") && !strings.HasPrefix(trimmed, "require (") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				all[fields[1]] = struct{}{}
				direct[fields[1]] = struct{}{}
			}
			continue
		}
		if inRequire && trimmed == ")" {
			inRequire = false
			block++
			continue
		}
		if !inRequire || trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		path := fields[0]
		all[path] = struct{}{}
		if block == 0 {
			direct[path] = struct{}{}
		}
	}
	if len(direct) == 0 {
		t.Fatal("go.mod first require block was empty")
	}
	return goModModules{direct: direct, all: all}
}
