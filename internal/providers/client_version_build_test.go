package providers

import (
	"strings"
	"testing"

	"autoto/internal/config"
)

// A release build may inject the version with
// -ldflags "-X autoto/internal/config.Version=...". That value reaches every
// provider as ClientVersion and is validated as strict semver, so a non-semver
// value makes validateProviderRuntimeConfig fail for every provider at once.
// The visible result is not an error dialog: /api/models reports Available
// false for everything and the setup wizard says no usable models exist.
// A desktop build shipped with Version=dev and reproduced exactly that.
func TestClientVersionRejectsNonSemverBuildStamps(t *testing.T) {
	rejected := []string{"dev", "development", "v1.2.3", "1.2", "latest", "dev build"}
	for _, value := range rejected {
		if err := validateClientVersion(value); err == nil {
			t.Errorf("validateClientVersion(%q) accepted a non-semver build stamp", value)
		}
	}

	accepted := []string{"1.0.0-dev", "0.1.0-dev", "1.2.3", "0.0.1", "1.2.3-rc.1", "1.2.3+build.5", ""}
	for _, value := range accepted {
		if err := validateClientVersion(value); err != nil {
			t.Errorf("validateClientVersion(%q) rejected a valid version: %v", value, err)
		}
	}
}

// The compiled-in default must itself satisfy the provider identity check,
// otherwise a stock build cannot construct any provider.
func TestCompiledVersionSatisfiesProviderIdentity(t *testing.T) {
	if err := validateClientVersion(config.Version); err != nil {
		t.Fatalf("config.Version %q fails provider identity validation, so no provider can be built: %v", config.Version, err)
	}
	if err := validateProviderRuntimeIdentity(config.ProviderConfig{ClientVersion: config.Version}); err != nil {
		t.Fatalf("config.Version %q fails runtime identity validation: %v", config.Version, err)
	}
}

// The adapter must neutralise unusable stamps and pass valid ones through.
func TestClientVersionFromBuildStamp(t *testing.T) {
	// Real `git describe` output from this repository, which broke every provider.
	for _, stamp := range []string{
		"windows-preview-20260722-220-g778feef-dirty",
		"dev",
		"v1.2.3",
		"1.2",
		"1.2.3\r\nInjected: header",
	} {
		if got := ClientVersionFromBuildStamp(stamp); got != "" {
			t.Errorf("ClientVersionFromBuildStamp(%q) = %q, want empty", stamp, got)
		}
	}
	for _, stamp := range []string{"1.0.0-dev", "0.1.0-dev", "1.2.3", "1.2.3-rc.1"} {
		if got := ClientVersionFromBuildStamp(stamp); got != stamp {
			t.Errorf("ClientVersionFromBuildStamp(%q) = %q, want unchanged", stamp, got)
		}
	}
	// The neutralised value must be acceptable downstream, otherwise the adapter
	// only moves the failure.
	if err := validateClientVersion(ClientVersionFromBuildStamp("dev")); err != nil {
		t.Fatalf("adapted stamp still fails validation: %v", err)
	}
	if err := validateProviderRuntimeConfig(config.ProviderConfig{
		Name:          "anthropic",
		Type:          "anthropic",
		ClientVersion: ClientVersionFromBuildStamp("dev"),
	}); err != nil {
		t.Fatalf("provider construction still fails after adaptation: %v", err)
	}
}

// Guard the failure path itself: an invalid stamp must surface as a config error
// on a constructed provider rather than being silently ignored.
func TestInvalidClientVersionFailsProviderConstruction(t *testing.T) {
	err := validateProviderRuntimeConfig(config.ProviderConfig{
		Name:          "anthropic",
		Type:          "anthropic",
		ClientVersion: "dev",
	})
	if err == nil {
		t.Fatal("provider runtime config accepted ClientVersion=dev")
	}
	if !strings.Contains(err.Error(), "client version") {
		t.Fatalf("error does not name the client version, so the cause would be hard to find: %v", err)
	}
}
