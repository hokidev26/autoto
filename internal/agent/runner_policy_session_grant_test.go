package agent

import (
	"encoding/json"
	"testing"
)

// A human approving a script for the session approved that script, not one exact
// argument list. Re-prompting because a line range changed made the session
// choice useless for parameterized scripts.
func TestSessionGrantKeyAnchorsOnScriptPath(t *testing.T) {
	keyFor := func(command string) string {
		input, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatalf("marshal command: %v", err)
		}
		return sessionGrantKey("Bash", input)
	}

	base := keyFor(`powershell -File tools/read.ps1 -Start 604 -End 636`)
	if base == "" {
		t.Fatal("expected a reusable grant key for a script invocation")
	}
	if other := keyFor(`powershell -File tools/read.ps1 -Start 12 -End 40`); other != base {
		t.Fatalf("same script with different parameters must reuse the grant: %q != %q", other, base)
	}
	if quoted := keyFor(`powershell -File "tools/read.ps1" -Start 1 -End 2`); quoted == base {
		t.Log("quoted path yields its own key; acceptable because the token differs verbatim")
	}

	if scriptSwap := keyFor(`powershell -File tools/write.ps1 -Start 604 -End 636`); scriptSwap == base {
		t.Fatal("a different script must not inherit the grant")
	}
	if interpreterSwap := keyFor(`pwsh -File tools/read.ps1 -Start 604 -End 636`); interpreterSwap == base {
		t.Fatal("a different interpreter must not inherit the grant")
	}
	if chained := keyFor(`cd repo && powershell -File tools/read.ps1 -Start 1 -End 2`); chained == base {
		t.Fatal("a compound prefix changes what runs and must not inherit the grant")
	}

	plain := keyFor(`go test ./...`)
	if plain != keyFor(`  go   test   ./...  `) {
		t.Fatal("non-script commands must still normalize whitespace to one key")
	}
	if plain == base {
		t.Fatal("unrelated commands must not share a key")
	}
}
