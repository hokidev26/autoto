package config

import "testing"

func TestParseClientIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: " autoto ", want: ""},
		{in: "AUTOTO", want: ""},
		{in: "claude-code", want: ClientIdentityClaudeCode},
		{in: " Claude-Code ", want: ClientIdentityClaudeCode},
		{in: "codex", want: ClientIdentityCodex},
		{in: "not-a-cli", wantErr: true},
	}
	for _, test := range tests {
		got, err := ParseClientIdentity(test.in)
		if test.wantErr {
			if err == nil {
				t.Fatalf("ParseClientIdentity(%q) succeeded, want error", test.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseClientIdentity(%q): %v", test.in, err)
		}
		if got != test.want {
			t.Fatalf("ParseClientIdentity(%q)=%q, want %q", test.in, got, test.want)
		}
	}
}

func TestNormalizeClientIdentityFailsClosed(t *testing.T) {
	t.Parallel()
	if got := NormalizeClientIdentity("evil"); got != "" {
		t.Fatalf("unknown identity should fall back to Autoto, got %q", got)
	}
	if got := NormalizeClientIdentity("codex"); got != ClientIdentityCodex {
		t.Fatalf("NormalizeClientIdentity(codex)=%q", got)
	}
}
