package providers

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"autoto/internal/config"
)

var (
	ErrProviderUnavailable        = errors.New("provider unavailable")
	ErrReasoningEffortUnsupported = errors.New("reasoning effort is not supported")
	ErrFastModeUnsupported        = errors.New("fast mode is not supported")
)

var clientVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?(?:\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

func providerUnavailableError(providerName, detail string) error {
	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		providerName = "model"
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return fmt.Errorf("%w: %s provider is unavailable", ErrProviderUnavailable, providerName)
	}
	return fmt.Errorf("%w: %s provider is unavailable: %s", ErrProviderUnavailable, providerName, detail)
}

var legacyReasoningEfforts = []string{"low", "medium", "high"}

// canonicalReasoningEfforts is the full vocabulary, ordered weakest to
// strongest. It defines what may be stored and validated, not what any given
// provider offers.
var canonicalReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

// codexBaselineReasoningEfforts is what every Codex model serves. "max" and
// "ultra" exist on some models only — the authenticated catalog reports the
// exact set per model — so advertising them provider-wide would offer a level
// the chosen model answers with HTTP 400. ModelCapabilities.ReasoningEfforts
// carries the per-model truth and wins over this baseline.
var codexBaselineReasoningEfforts = []string{"low", "medium", "high", "xhigh"}

// anthropicBaselineReasoningEfforts is the widest set any Anthropic model
// serves. Adaptive models (4.6+) forward the effort verbatim to output_config,
// and the SDK defines xhigh and max as valid values there. Per-model narrowing
// lives in AnthropicProvider.ModelCapabilities: xhigh needs 4.7+, and models
// still on the manual budget path serve only low/medium/high.
var anthropicBaselineReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// CapabilitiesForConfig derives protocol capabilities from a provider's static
// configuration, without needing a live registered instance. Model-catalog
// clients gate the thinking-effort picker on the advertised effort list, so a
// provider that is configured but not yet registered would otherwise report no
// effort support and collapse the picker to "auto" only.
//
// Values must mirror each adapter's Capabilities() implementation.
func CapabilitiesForConfig(cfg config.ProviderConfig) Capabilities {
	capabilities := Capabilities{Tools: true, Streaming: true}
	switch strings.TrimSpace(cfg.Type) {
	case "anthropic":
		capabilities.ImageInput = true
		capabilities.Reasoning = true
		capabilities.ReasoningEfforts = anthropicBaselineReasoningEfforts
	case config.ProviderTypeCodex:
		capabilities.ImageInput = true
		capabilities.ImageGeneration = true
		capabilities.ReasoningEfforts = codexBaselineReasoningEfforts
	case "openai":
		capabilities.ImageInput = true
		capabilities.ImageGeneration = true
		capabilities.ReasoningEfforts = legacyReasoningEfforts
	case "openai-compatible":
		capabilities.ImageInput = cfg.ImageInput
		if cfg.Profile == config.ProviderProfileCLIProxyAPI {
			capabilities.ReasoningEfforts = legacyReasoningEfforts
		}
	case config.ProviderTypeGemini:
		capabilities.ImageInput = true
		capabilities.ImageGeneration = true
		capabilities.Reasoning = true
		capabilities.ReasoningEfforts = legacyReasoningEfforts
	case config.ProviderTypeGeminiInteractions:
		capabilities.ImageInput = true
		capabilities.Reasoning = true
		capabilities.ReasoningEfforts = legacyReasoningEfforts
	case config.ProviderTypeGrok:
		capabilities.ImageInput = true
		capabilities.ReasoningEfforts = legacyReasoningEfforts
	case config.ProviderTypeKimi:
		capabilities.ImageInput = true
		capabilities.ReasoningEfforts = []string{"low", "high"}
	case config.ProviderTypeKiro:
		// Kiro advertises no thinking-effort control.
	default:
		return Capabilities{}
	}
	return canonicalCapabilities(capabilities)
}

// canonicalCapabilities preserves the legacy boolean capability while exposing
// a canonical values list to model-catalog clients. A legacy true boolean means
// the historical low/medium/high set, never xhigh.
func canonicalCapabilities(capabilities Capabilities) Capabilities {
	values := canonicalReasoningEffortValues(capabilities.ReasoningEfforts)
	if capabilities.Reasoning {
		capabilities.ReasoningEffort = true
	}
	if len(values) == 0 && capabilities.ReasoningEffort {
		values = append([]string(nil), legacyReasoningEfforts...)
	}
	if len(values) == 0 {
		capabilities.ReasoningEffort = false
		capabilities.ReasoningEfforts = nil
		return capabilities
	}
	capabilities.ReasoningEffort = true
	capabilities.ReasoningEfforts = values
	return capabilities
}

func canonicalReasoningEffortValues(values []string) []string {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		for _, known := range canonicalReasoningEfforts {
			if value == known {
				seen[value] = true
				break
			}
		}
	}
	out := make([]string, 0, len(seen))
	for _, known := range canonicalReasoningEfforts {
		if seen[known] {
			out = append(out, known)
		}
	}
	return out
}

// SupportsReasoningEffort reports whether a provider can accept a concrete
// non-auto reasoning effort. Empty and auto are always safe because adapters
// omit the upstream reasoning parameter for those values.
func (capabilities Capabilities) SupportsReasoningEffort(raw string) bool {
	effort := strings.ToLower(strings.TrimSpace(raw))
	if effort == "" || effort == "auto" {
		return true
	}
	for _, value := range canonicalCapabilities(capabilities).ReasoningEfforts {
		if effort == value {
			return true
		}
	}
	return false
}

func normalizeReasoningEffort(raw string, supported bool, providerName string) (string, error) {
	return normalizeReasoningEffortForCapabilities(raw, Capabilities{ReasoningEffort: supported}, providerName)
}

func normalizeReasoningEffortForCapabilities(raw string, capabilities Capabilities, providerName string) (string, error) {
	effort := strings.ToLower(strings.TrimSpace(raw))
	switch effort {
	case "", "auto":
		return "", nil
	case "low", "medium", "high", "xhigh", "max", "ultra":
		if canonicalCapabilities(capabilities).SupportsReasoningEffort(effort) {
			return effort, nil
		}
		return "", fmt.Errorf("%w by %s provider (requested %q)", ErrReasoningEffortUnsupported, strings.TrimSpace(providerName), effort)
	default:
		return "", fmt.Errorf("invalid reasoning effort %q: supported values are auto, low, medium, high, xhigh, max, and ultra", raw)
	}
}

func validateProviderRuntimeIdentity(cfg config.ProviderConfig) error {
	if err := validateClientVersion(cfg.ClientVersion); err != nil {
		return err
	}
	if err := validateInstallationID(cfg.InstallationID); err != nil {
		return err
	}
	return nil
}

// ClientVersionFromBuildStamp adapts a build stamp such as config.Version into a
// value that is safe to place in ProviderConfig.ClientVersion.
//
// The stamp is a human-facing string with no format guarantee: release tooling
// derives it from `git describe`, which yields values like
// "windows-preview-20260722-220-g778feef-dirty". ClientVersion is validated as
// strict semver, so assigning the stamp directly made every provider fail
// construction at once, and the only visible symptom was an empty model list.
//
// An unusable stamp therefore degrades to "", which validateClientVersion
// accepts and autotoClientHeaderValue renders as "omit the header". This is a
// deliberate one-way adapter for trusted internal build metadata; it must not be
// used on configured or remote input, where a rejected value has to stay an
// error so header injection cannot be laundered into a silent fallback.
func ClientVersionFromBuildStamp(stamp string) string {
	if validateClientVersion(stamp) != nil {
		return ""
	}
	return stamp
}

func validateClientVersion(value string) error {
	if value == "" {
		return nil
	}
	if value != strings.TrimSpace(value) || len(value) > 64 || !clientVersionPattern.MatchString(value) {
		return fmt.Errorf("invalid Autoto client version %q: expected a semantic version without whitespace", value)
	}
	return nil
}

func validateInstallationID(value string) error {
	if value == "" {
		return nil
	}
	if value != strings.TrimSpace(value) || len(value) != 36 {
		return fmt.Errorf("invalid Autoto installation ID %q: expected a canonical UUIDv4", value)
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value || parsed.Version() != uuid.Version(4) || parsed.Variant() != uuid.RFC4122 {
		return fmt.Errorf("invalid Autoto installation ID %q: expected a canonical UUIDv4", value)
	}
	return nil
}

func autotoClientHeaderValue(cfg config.ProviderConfig) string {
	if cfg.ClientVersion == "" {
		return ""
	}
	return "autoto/" + cfg.ClientVersion
}
