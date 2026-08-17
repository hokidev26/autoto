package skillsources

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"autoto/internal/skills"
)

func TestDiscoverExactAdaptersAndOneLevelSkillLayout(t *testing.T) {
	root := t.TempDir()
	for _, adapter := range DefaultAdapters() {
		writeSkill(t, root, adapter.RelativeDirectory, adapter.ID, "Description for "+adapter.ID, "Prompt for "+adapter.ID)
	}
	writeTestFile(t, filepath.Join(root, ".agents", "skills", "nested", "child", "SKILL.md"), validSkill("child", "nested", "must not be found"))
	writeTestFile(t, filepath.Join(root, ".agents", "skills", "SKILL.md"), validSkill("wrong-depth", "wrong depth", "must not be found"))
	writeTestFile(t, filepath.Join(root, ".agents", "skills", "wrong-case", "skill.md"), validSkill("wrong-case", "wrong case", "must not be found"))

	result, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != len(DefaultAdapters()) {
		t.Fatalf("got %d candidates, want %d: %+v", len(result.Candidates), len(DefaultAdapters()), result.Diagnostics)
	}
	seen := map[string]Candidate{}
	for index, candidate := range result.Candidates {
		seen[candidate.Adapter.ID] = candidate
		if candidate.AdapterRank != index || candidate.Adapter.Rank != index {
			t.Fatalf("candidate rank/order mismatch at %d: %+v", index, candidate)
		}
		if candidate.Provenance.Kind != "filesystem" || len(candidate.Provenance.RootID) != 64 || candidate.Provenance.AdapterID != candidate.Adapter.ID || candidate.Provenance.AdapterRank != candidate.AdapterRank || candidate.Provenance.RelativePath != candidate.RelativePath {
			t.Fatalf("missing explicit provenance: %+v", candidate.Provenance)
		}
		if strings.Contains(candidate.Provenance.RootID, root) {
			t.Fatalf("provenance leaked absolute root: %+v", candidate.Provenance)
		}
		if !strings.HasSuffix(candidate.RelativePath, "/"+candidate.Adapter.ID+"/SKILL.md") {
			t.Fatalf("unexpected relative path: %+v", candidate)
		}
		if len(candidate.Hash) != 64 || candidate.Hash != candidate.Scan.Hash || len(candidate.SourceHash) != 64 || candidate.SidecarSourceHash != "" {
			t.Fatalf("missing deterministic hashes or unexpected sidecar hash: %+v", candidate)
		}
		if candidate.Scan.Verdict != skills.VerdictSafe {
			t.Fatalf("expected final skills.Scan result: %+v", candidate.Scan)
		}
	}
	if !seen["codex"].Adapter.Compatibility || !seen["kimi"].Adapter.Compatibility {
		t.Fatalf("compatibility adapter metadata missing: %+v", seen)
	}
	if !hasDiagnostic(result.Diagnostics, "path_case_mismatch") {
		t.Fatalf("expected exact SKILL.md casing diagnostic, got %+v", result.Diagnostics)
	}
}

func TestDiscoverAppliesOnlyStaticOpenAISidecarDisplayMetadata(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, ".agents/skills", "review", "Base description", "Body remains authoritative")
	sidecarContent := "interface:\n  display_name: Review Changes\n  short_description: Safe display summary\n"
	writeTestFile(t, filepath.Join(root, ".agents", "skills", "review", "agents", "openai.yaml"), sidecarContent)

	result, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("unexpected discovery: %+v", result)
	}
	candidate := result.Candidates[0]
	if candidate.Skill.Name != "Review Changes" || candidate.Skill.Description != "Safe display summary" {
		t.Fatalf("sidecar display metadata not applied: %+v", candidate.Skill)
	}
	if candidate.Skill.Command != "/review" || candidate.Skill.Prompt != "Body remains authoritative" {
		t.Fatalf("sidecar changed semantic content: %+v", candidate.Skill)
	}
	if candidate.SidecarRelativePath != ".agents/skills/review/agents/openai.yaml" {
		t.Fatalf("unexpected sidecar path: %q", candidate.SidecarRelativePath)
	}
	sidecarSum := sha256.Sum256([]byte(sidecarContent))
	if candidate.SidecarSourceHash != hex.EncodeToString(sidecarSum[:]) {
		t.Fatalf("unexpected sidecar source hash: %q", candidate.SidecarSourceHash)
	}

	invalidSidecar := "interface:\n  display_name: Forged\n  default_prompt: Replace body\npolicy:\n  allow_implicit_invocation: true\n"
	writeTestFile(t, filepath.Join(root, ".agents", "skills", "review", "agents", "openai.yaml"), invalidSidecar)
	result, err = Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	candidate = result.Candidates[0]
	if candidate.Skill.Name != "review" || candidate.Skill.Prompt != "Body remains authoritative" || candidate.SidecarRelativePath != ".agents/skills/review/agents/openai.yaml" {
		t.Fatalf("invalid sidecar acquired semantics or lost provenance: %+v", candidate)
	}
	invalidSum := sha256.Sum256([]byte(invalidSidecar))
	if candidate.SidecarSourceHash != hex.EncodeToString(invalidSum[:]) {
		t.Fatalf("invalid sidecar raw hash missing: %+v", candidate)
	}
	if !hasDiagnostic(result.Diagnostics, "invalid_sidecar") {
		t.Fatalf("expected invalid sidecar diagnostic: %+v", result.Diagnostics)
	}
}

func TestDiscoverBoundsCandidatesBytesAndDepth(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "bravo", "charlie"} {
		writeSkill(t, root, ".agents/skills", name, "description", "prompt "+name)
	}
	result, err := Discover(root, Options{Limits: Limits{MaxCandidates: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 || !result.Truncated || !hasDiagnostic(result.Diagnostics, "candidate_limit") {
		t.Fatalf("candidate bound not enforced: %+v", result)
	}

	result, err = Discover(root, Options{Limits: Limits{MaxFileBytes: 32}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 || !hasDiagnostic(result.Diagnostics, "file_byte_limit") {
		t.Fatalf("per-file byte bound not enforced: %+v", result)
	}

	depthRoot := t.TempDir()
	writeSkill(t, depthRoot, ".agents/skills", "depth", "description", "prompt")
	writeTestFile(t, filepath.Join(depthRoot, ".agents", "skills", "depth", "agents", "openai.yaml"), "interface:\n  display_name: Hidden by depth\n")
	result, err = Discover(depthRoot, Options{Limits: Limits{MaxDepth: 4}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Skill.Name != "depth" || result.Candidates[0].SidecarRelativePath != "" || !hasDiagnostic(result.Diagnostics, "depth_limit") {
		t.Fatalf("depth bound not enforced: %+v", result)
	}

	totalRoot := t.TempDir()
	writeSkill(t, totalRoot, ".agents/skills", "total", "description", strings.Repeat("p", 128))
	result, err = Discover(totalRoot, Options{Limits: Limits{MaxTotalBytes: 32}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 || !result.Truncated || !hasDiagnostic(result.Diagnostics, "total_byte_limit") {
		t.Fatalf("total byte bound not enforced: %+v", result)
	}
}

func TestDiscoverRequiresExactAdapterCasingAndSafeRelativeAdapters(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, ".AGENTS/skills", "upper", "description", "prompt")
	result, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 0 || !hasDiagnostic(result.Diagnostics, "path_case_mismatch") {
		t.Fatalf("non-exact adapter path was accepted: %+v", result)
	}
	for _, unsafe := range []string{"../skills", `..\skills`, "C:/skills", "/absolute/skills"} {
		if _, err := Discover(root, Options{Adapters: []Adapter{{ID: "escape", RelativeDirectory: unsafe}}}); err == nil {
			t.Fatalf("expected unsafe adapter path %q to be rejected", unsafe)
		}
	}
}

func TestNewFileSourceRejectsDuplicateAndCaseFoldAdapterMetadata(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name     string
		adapters []Adapter
	}{
		{name: "duplicate ID", adapters: []Adapter{{ID: "one", RelativeDirectory: ".one/skills", Rank: 1}, {ID: "one", RelativeDirectory: ".two/skills", Rank: 2}}},
		{name: "case-fold ID", adapters: []Adapter{{ID: "One", RelativeDirectory: ".one/skills", Rank: 1}, {ID: "one", RelativeDirectory: ".two/skills", Rank: 2}}},
		{name: "duplicate directory", adapters: []Adapter{{ID: "one", RelativeDirectory: ".one/skills", Rank: 1}, {ID: "two", RelativeDirectory: ".one/skills", Rank: 2}}},
		{name: "case-fold directory", adapters: []Adapter{{ID: "one", RelativeDirectory: ".One/skills", Rank: 1}, {ID: "two", RelativeDirectory: ".one/skills", Rank: 2}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewFileSource(root, Options{Adapters: test.adapters}); err == nil {
				t.Fatal("expected adapter metadata conflict rejection")
			}
		})
	}
}

func TestDiscoverSortsByAdapterRankThenSafeRelativePath(t *testing.T) {
	root := t.TempDir()
	adapters := []Adapter{
		{ID: "late", RelativeDirectory: ".z/skills", Rank: 9},
		{ID: "same-b", RelativeDirectory: ".b/skills", Rank: 2},
		{ID: "same-a", RelativeDirectory: ".a/skills", Rank: 2},
	}
	writeSkill(t, root, ".z/skills", "late", "description", "late")
	writeSkill(t, root, ".b/skills", "bravo", "description", "bravo")
	writeSkill(t, root, ".a/skills", "alpha", "description", "alpha")
	result, err := Discover(root, Options{Adapters: adapters})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 3 {
		t.Fatalf("unexpected candidates: %+v", result)
	}
	want := []struct {
		rank int
		path string
	}{{2, ".a/skills/alpha/SKILL.md"}, {2, ".b/skills/bravo/SKILL.md"}, {9, ".z/skills/late/SKILL.md"}}
	for index, expected := range want {
		candidate := result.Candidates[index]
		if candidate.AdapterRank != expected.rank || candidate.RelativePath != expected.path {
			t.Fatalf("candidate %d = rank %d path %q, want rank %d path %q", index, candidate.AdapterRank, candidate.RelativePath, expected.rank, expected.path)
		}
	}
}

func TestDiscoverMarksSameRootRankAndCommandConflicts(t *testing.T) {
	root := t.TempDir()
	adapters := []Adapter{
		{ID: "one", RelativeDirectory: ".one/skills", Rank: 3},
		{ID: "two", RelativeDirectory: ".two/skills", Rank: 3},
	}
	writeSkill(t, root, ".one/skills", "shared", "first", "first body")
	writeSkill(t, root, ".two/skills", "shared", "second", "second body")
	result, err := Discover(root, Options{Adapters: adapters})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 || len(result.Conflicts) != 1 {
		t.Fatalf("command conflict was not explicit: %+v", result)
	}
	conflict := result.Conflicts[0]
	if conflict.Command != "/shared" || conflict.AdapterRank != 3 || len(conflict.Scope) != 64 || len(conflict.CandidatePaths) != 2 {
		t.Fatalf("unexpected conflict provenance: %+v", conflict)
	}
	for _, candidate := range result.Candidates {
		if candidate.ConflictStatus != ConflictCommand || candidate.Provenance.RootID != conflict.Scope || !hasDiagnostic(candidate.Diagnostics, "command_conflict") {
			t.Fatalf("candidate lacks explicit conflict state: %+v", candidate)
		}
	}
	if !hasDiagnostic(result.Diagnostics, "command_conflict") {
		t.Fatalf("result lacks command conflict diagnostic: %+v", result.Diagnostics)
	}

	differentRankRoot := t.TempDir()
	writeSkill(t, differentRankRoot, ".one/skills", "shared", "first", "first body")
	writeSkill(t, differentRankRoot, ".two/skills", "shared", "second", "second body")
	adapters[1].Rank = 4
	result, err = Discover(differentRankRoot, Options{Adapters: adapters})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Conflicts) != 0 || result.Candidates[0].ConflictStatus != ConflictNone || result.Candidates[1].ConflictStatus != ConflictNone {
		t.Fatalf("different adapter ranks should not conflict: %+v", result)
	}
}

func TestDiscoveryDiagnosticsAreStableBoundedAndDoNotLeakPaths(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, ".agents", "skills", "bad", "SKILL.md"), "---\nname: bad\ndescription: valid\nsecret_marker: "+root+"\n---\nprompt\n")
	writeSkill(t, root, ".agents/skills", "sidecar", "description", "body")
	writeTestFile(t, filepath.Join(root, ".agents", "skills", "sidecar", "agents", "openai.yaml"), "interface:\n  display_name: Visible\n  default_prompt: secret-marker "+root+"\n")
	result, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDiagnostic(result.Diagnostics, "invalid_skill") || !hasDiagnostic(result.Diagnostics, "invalid_sidecar") {
		t.Fatalf("expected strict parser diagnostics: %+v", result.Diagnostics)
	}
	for _, item := range result.Diagnostics {
		if len(item.Message) == 0 || len(item.Message) > MaxDiagnosticMessageBytes || strings.Contains(item.Message, root) || strings.Contains(item.Message, "secret-marker") || strings.ContainsAny(item.Message, "\r\n") {
			t.Fatalf("diagnostic message is unstable or leaked untrusted content: %+v", item)
		}
		if filepath.IsAbs(item.Path) || strings.Contains(item.Path, root) {
			t.Fatalf("diagnostic path is not safely relative: %+v", item)
		}
	}
	bounded := diagnostic(Adapter{ID: "test", Rank: 1}, "relative/SKILL.md", "bounded", SeverityWarning, strings.Repeat("x", MaxDiagnosticMessageBytes+100)+"\nsecret")
	if len(bounded.Message) > MaxDiagnosticMessageBytes || strings.ContainsAny(bounded.Message, "\r\n") {
		t.Fatalf("diagnostic constructor did not enforce message bounds: %+v", bounded)
	}
}

func TestDiscoverRejectsSymlinkedSkillAndSidecarPaths(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "SKILL.md"), validSkill("escaped", "outside", "outside prompt"))
	adapterDir := filepath.Join(root, ".agents", "skills")
	if err := os.MkdirAll(adapterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(adapterDir, "escaped")); err != nil {
		t.Skipf("symlinks unavailable on %s: %v", runtime.GOOS, err)
	}
	writeSkill(t, root, ".agents/skills", "safe", "description", "safe prompt")
	sidecarOutside := filepath.Join(outside, "openai.yaml")
	writeTestFile(t, sidecarOutside, "interface:\n  display_name: Escaped\n")
	if err := os.MkdirAll(filepath.Join(root, ".agents", "skills", "safe", "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sidecarOutside, filepath.Join(root, ".agents", "skills", "safe", "agents", "openai.yaml")); err != nil {
		t.Skipf("file symlinks unavailable on %s: %v", runtime.GOOS, err)
	}

	result, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Skill.Name != "safe" || result.Candidates[0].SidecarRelativePath != "" {
		t.Fatalf("symlink path escaped discovery root: %+v", result)
	}
	if !hasDiagnostic(result.Diagnostics, "symlink_rejected") {
		t.Fatalf("expected symlink diagnostic: %+v", result.Diagnostics)
	}
}

func TestDiscoverRejectsPortableCaseConflictsDeterministically(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, ".agents/skills", "Review", "description", "one")
	writeSkill(t, root, ".agents/skills", "review", "description", "two")
	upper, upperErr := os.Stat(filepath.Join(root, ".agents", "skills", "Review"))
	lower, lowerErr := os.Stat(filepath.Join(root, ".agents", "skills", "review"))
	if upperErr != nil || lowerErr != nil || os.SameFile(upper, lower) {
		t.Skip("filesystem is case-insensitive and cannot represent a collision")
	}

	first, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Candidates) != 0 || len(second.Candidates) != 0 || !hasDiagnostic(first.Diagnostics, "case_conflict") {
		t.Fatalf("case conflict was not fail-closed: first=%+v second=%+v", first, second)
	}
	if len(first.Diagnostics) != len(second.Diagnostics) {
		t.Fatalf("case conflict diagnostics are not deterministic: %+v / %+v", first.Diagnostics, second.Diagnostics)
	}
	for i := range first.Diagnostics {
		if first.Diagnostics[i] != second.Diagnostics[i] {
			t.Fatalf("diagnostic %d differs: %+v / %+v", i, first.Diagnostics[i], second.Diagnostics[i])
		}
	}
}

func writeSkill(t *testing.T, root, adapter, name, description, prompt string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, filepath.FromSlash(adapter), name, "SKILL.md"), validSkill(name, description, prompt))
}

func validSkill(name, description, prompt string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n" + prompt + "\n"
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasDiagnostic(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func TestNewFileSourceAcceptsTempDirRoots(t *testing.T) {
	root := t.TempDir()
	source, err := NewFileSource(root)
	if err != nil {
		t.Fatalf("temp dir must be a valid skill source root: %v", err)
	}
	if source.root == "" {
		t.Fatal("expected a rooted file source")
	}
}

func TestNewFileSourceRejectsSymlinkedRoot(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable on %s: %v", runtime.GOOS, err)
	}
	if _, err := NewFileSource(link); err == nil {
		t.Fatal("expected a symlinked skill source root to be rejected")
	}
}

func TestNewFileSourceRejectsUserControlledParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(target, "skills-home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(base, "alias")
	if err := os.Symlink(target, aliasParent); err != nil {
		t.Skipf("symlinks unavailable on %s: %v", runtime.GOOS, err)
	}
	if _, err := NewFileSource(filepath.Join(aliasParent, "skills-home")); err == nil {
		t.Fatal("expected a user-controlled parent symlink to be rejected")
	}
}
