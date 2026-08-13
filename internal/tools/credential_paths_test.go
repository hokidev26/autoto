package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSensitiveCredentialPathsRejected extends the workspace denylist to common
// credential stores beyond the individual key files already covered: SSH and AWS
// credential directories, the Kubernetes and Docker configs, and the git
// credential store. Path tools must reject them and search tools must omit them,
// while a file that merely resembles a sensitive name stays accessible.
func TestSensitiveCredentialPathsRejected(t *testing.T) {
	cwd := t.TempDir()
	const secret = "TOP_SECRET_CREDENTIAL"
	sensitive := []string{
		filepath.Join(".ssh", "id_rsa"),
		filepath.Join(".ssh", "config"),
		filepath.Join(".ssh", "known_hosts"),
		filepath.Join(".aws", "credentials"),
		filepath.Join(".aws", "config"),
		filepath.Join(".kube", "config"),
		filepath.Join(".docker", "config.json"),
		".git-credentials",
		filepath.Join("project", ".ssh", "id_ecdsa"),
	}
	for _, name := range sensitive {
		full := filepath.Join(cwd, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(secret), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveInCWD(cwd, name); err == nil || !strings.Contains(err.Error(), "sensitive path") {
			t.Errorf("expected sensitive rejection for %q, got %v", name, err)
		}
		readInput, _ := json.Marshal(map[string]any{"file_path": name})
		if res, _ := (ReadTool{}).Execute(context.Background(), Call{ID: "r", Name: "Read", Input: readInput}, Env{CWD: cwd}); !res.IsError || !strings.Contains(res.Output, "sensitive path") {
			t.Errorf("Read must reject %q, got %+v", name, res)
		}
		writeInput, _ := json.Marshal(map[string]any{"file_path": name, "content": "x"})
		if res, _ := (WriteTool{}).Execute(context.Background(), Call{ID: "w", Name: "Write", Input: writeInput}, Env{CWD: cwd}); !res.IsError || !strings.Contains(res.Output, "sensitive path") {
			t.Errorf("Write must reject %q, got %+v", name, res)
		}
	}

	// A file that only resembles a sensitive name must stay accessible.
	control := "docker-compose.yml"
	if err := os.WriteFile(filepath.Join(cwd, control), []byte("services: {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveInCWD(cwd, control); err != nil {
		t.Errorf("control file %q must stay accessible, got %v", control, err)
	}

	grepInput, _ := json.Marshal(map[string]any{"pattern": secret, "path": "."})
	grepResult, err := (GrepTool{}).Execute(context.Background(), Call{ID: "grep", Name: "Grep", Input: grepInput}, Env{CWD: cwd})
	if err != nil || grepResult.IsError || grepResult.Output != "No matches found" {
		t.Fatalf("grep exposed credential content: result=%+v err=%v", grepResult, err)
	}
	globInput, _ := json.Marshal(map[string]any{"pattern": "**/*", "path": "."})
	globResult, err := (GlobTool{}).Execute(context.Background(), Call{ID: "glob", Name: "Glob", Input: globInput}, Env{CWD: cwd})
	if err != nil || globResult.IsError {
		t.Fatalf("glob failed: result=%+v err=%v", globResult, err)
	}
	for _, needle := range []string{".ssh", ".aws", ".kube", ".git-credentials", "config.json"} {
		if strings.Contains(globResult.Output, needle) {
			t.Errorf("glob leaked sensitive path %q: %s", needle, globResult.Output)
		}
	}
}
