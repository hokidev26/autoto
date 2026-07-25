package skills

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

const MaxOpenAISidecarBytes = 16 * 1024

// OpenAISidecar contains only the display-only subset of agents/openai.yaml
// consumed by Code Harbor. No prompt, tool, policy, dependency, or invocation
// field is represented here.
type OpenAISidecar struct {
	DisplayName      string `json:"displayName"`
	ShortDescription string `json:"shortDescription"`
}

// ParseOpenAISidecar strictly accepts this static subset:
//
// interface:
//
//	display_name: string
//	short_description: string
//
// Any other field is rejected so future or provider-specific permission fields
// cannot silently acquire semantics.
func ParseOpenAISidecar(content string) (OpenAISidecar, error) {
	if len(content) == 0 {
		return OpenAISidecar{}, errors.New("OpenAI skill sidecar content is required")
	}
	if len(content) > MaxOpenAISidecarBytes {
		return OpenAISidecar{}, fmt.Errorf("OpenAI skill sidecar exceeds %d bytes", MaxOpenAISidecarBytes)
	}
	mapping, err := decodeStrictYAMLMapping(normalizeNewlines(content))
	if err != nil {
		return OpenAISidecar{}, fmt.Errorf("invalid OpenAI skill sidecar: %w", err)
	}
	if len(mapping.Content) != 2 {
		return OpenAISidecar{}, errors.New("OpenAI skill sidecar must contain only the interface mapping")
	}
	key, err := strictYAMLString(mapping.Content[0])
	if err != nil || key != "interface" {
		return OpenAISidecar{}, errors.New("OpenAI skill sidecar must contain only the interface mapping")
	}
	interfaceNode := mapping.Content[1]
	if interfaceNode.Kind != yaml.MappingNode || interfaceNode.Tag != "!!map" {
		return OpenAISidecar{}, errors.New("OpenAI skill sidecar interface must be a mapping")
	}

	var parsed OpenAISidecar
	seen := map[string]struct{}{}
	for i := 0; i < len(interfaceNode.Content); i += 2 {
		field, fieldErr := strictYAMLString(interfaceNode.Content[i])
		if fieldErr != nil {
			return OpenAISidecar{}, errors.New("OpenAI skill sidecar interface keys must be strings")
		}
		if len(field) > MaxAgentSkillMetadataKeyBytes {
			return OpenAISidecar{}, fmt.Errorf("OpenAI skill sidecar field name exceeds %d bytes", MaxAgentSkillMetadataKeyBytes)
		}
		if _, exists := seen[field]; exists {
			return OpenAISidecar{}, fmt.Errorf("OpenAI skill sidecar has duplicate %q", field)
		}
		seen[field] = struct{}{}
		value, valueErr := strictYAMLString(interfaceNode.Content[i+1])
		if valueErr != nil {
			return OpenAISidecar{}, fmt.Errorf("OpenAI skill sidecar field %q must be a string", field)
		}
		value = strings.TrimSpace(value)
		switch field {
		case "display_name":
			parsed.DisplayName = value
		case "short_description":
			parsed.ShortDescription = value
		default:
			return OpenAISidecar{}, fmt.Errorf("OpenAI skill sidecar contains unsupported interface field %q", field)
		}
	}
	if parsed.DisplayName == "" && parsed.ShortDescription == "" {
		return OpenAISidecar{}, errors.New("OpenAI skill sidecar requires display_name or short_description")
	}
	if len(parsed.DisplayName) > MaxNameBytes {
		return OpenAISidecar{}, fmt.Errorf("OpenAI display_name exceeds %d bytes", MaxNameBytes)
	}
	if len(parsed.ShortDescription) > MaxDescriptionBytes {
		return OpenAISidecar{}, fmt.Errorf("OpenAI short_description exceeds %d bytes", MaxDescriptionBytes)
	}
	return parsed, nil
}
