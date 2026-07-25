package skills

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	MaxAgentSkillNameBytes          = 64
	MaxAgentSkillDescriptionBytes   = 1024
	MaxAgentSkillCompatibilityBytes = 500
	MaxAgentSkillMetadataEntries    = 64
	MaxAgentSkillMetadataKeyBytes   = 128
	MaxAgentSkillMetadataValueBytes = 1024
	MaxAgentSkillAllowedToolsBytes  = 2048
)

var agentSkillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// AgentSkillDiagnostic reports non-fatal metadata that was parsed but is not
// granted any execution semantics by Code Harbor.
type AgentSkillDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AgentSkill is the strictly parsed, still-untrusted representation of a
// SKILL.md document. Call NormalizeAgentSkill before persisting or exposing it.
type AgentSkill struct {
	Name          string
	Description   string
	License       string
	Compatibility string
	Metadata      map[string]string
	Prompt        string
	Diagnostics   []AgentSkillDiagnostic
}

// ParseAgentSkill parses the Agent Skills SKILL.md frontmatter. Unknown fields,
// duplicate keys, aliases, custom tags, non-string scalar values, and multiple
// YAML documents are rejected. The optional allowed-tools field is accepted as
// metadata but deliberately discarded with a diagnostic.
func ParseAgentSkill(content string) (AgentSkill, error) {
	if err := validateContent(content); err != nil {
		return AgentSkill{}, err
	}
	content = normalizeNewlines(content)
	frontmatter, body, err := splitAgentSkillFrontmatter(content)
	if err != nil {
		return AgentSkill{}, err
	}
	mapping, err := decodeStrictYAMLMapping(frontmatter)
	if err != nil {
		return AgentSkill{}, fmt.Errorf("invalid Agent Skill frontmatter: %w", err)
	}

	parsed := AgentSkill{Metadata: map[string]string{}}
	seen := make(map[string]struct{}, len(mapping.Content)/2)
	for i := 0; i < len(mapping.Content); i += 2 {
		keyNode, valueNode := mapping.Content[i], mapping.Content[i+1]
		key, err := strictYAMLString(keyNode)
		if err != nil {
			return AgentSkill{}, errors.New("Agent Skill frontmatter keys must be strings")
		}
		if len(key) > MaxAgentSkillMetadataKeyBytes {
			return AgentSkill{}, fmt.Errorf("Agent Skill frontmatter field name exceeds %d bytes", MaxAgentSkillMetadataKeyBytes)
		}
		if _, exists := seen[key]; exists {
			return AgentSkill{}, fmt.Errorf("Agent Skill frontmatter has duplicate %q", key)
		}
		seen[key] = struct{}{}

		switch key {
		case "name":
			parsed.Name, err = strictYAMLString(valueNode)
		case "description":
			parsed.Description, err = strictYAMLString(valueNode)
		case "license":
			parsed.License, err = strictYAMLString(valueNode)
		case "compatibility":
			parsed.Compatibility, err = strictYAMLString(valueNode)
		case "metadata":
			parsed.Metadata, err = parseAgentSkillMetadata(valueNode)
		case "allowed-tools":
			var ignored string
			ignored, err = strictYAMLString(valueNode)
			if err == nil && len(ignored) > MaxAgentSkillAllowedToolsBytes {
				err = fmt.Errorf("allowed-tools exceeds %d bytes", MaxAgentSkillAllowedToolsBytes)
			}
			if err == nil && strings.TrimSpace(ignored) != "" {
				parsed.Diagnostics = append(parsed.Diagnostics, AgentSkillDiagnostic{
					Code:    "allowed_tools_ignored",
					Message: "Agent Skill allowed-tools metadata is untrusted and was ignored.",
				})
			}
		default:
			return AgentSkill{}, fmt.Errorf("Agent Skill frontmatter contains unsupported field %q", key)
		}
		if err != nil {
			return AgentSkill{}, fmt.Errorf("Agent Skill frontmatter field %q: %w", key, err)
		}
	}

	parsed.Name = strings.TrimSpace(parsed.Name)
	parsed.Description = strings.TrimSpace(parsed.Description)
	parsed.License = strings.TrimSpace(parsed.License)
	parsed.Compatibility = strings.TrimSpace(parsed.Compatibility)
	parsed.Prompt = body
	if err := validateAgentSkillMetadata(parsed); err != nil {
		return AgentSkill{}, err
	}
	return parsed, nil
}

// ParseAgentSkillForDirectory additionally enforces the portable Agent Skills
// invariant that the skill directory name matches the frontmatter name.
func ParseAgentSkillForDirectory(content, directoryName string) (AgentSkill, error) {
	parsed, err := ParseAgentSkill(content)
	if err != nil {
		return AgentSkill{}, err
	}
	if directoryName != parsed.Name {
		return AgentSkill{}, fmt.Errorf("Agent Skill name %q must match directory %q", parsed.Name, directoryName)
	}
	return parsed, nil
}

// NormalizeAgentSkill applies display-only OpenAI sidecar metadata and then
// delegates final persisted semantics to the existing Normalize function. The
// command always comes from the trusted grammar of the Agent Skill name, and
// the sidecar can never replace Prompt.
func NormalizeAgentSkill(document AgentSkill, sidecar *OpenAISidecar) (Skill, error) {
	name := document.Name
	description := document.Description
	if sidecar != nil {
		if sidecar.DisplayName != "" {
			name = sidecar.DisplayName
		}
		if sidecar.ShortDescription != "" {
			description = sidecar.ShortDescription
		}
	}
	return Normalize(Skill{
		Name:        name,
		Command:     "/" + document.Name,
		Description: description,
		Prompt:      document.Prompt,
	})
}

func splitAgentSkillFrontmatter(content string) (string, string, error) {
	if !strings.HasPrefix(content, "---\n") {
		return "", "", errors.New("Agent Skill must begin with YAML frontmatter")
	}
	lines := strings.Split(content, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "---" {
			continue
		}
		body := strings.Join(lines[i+1:], "\n")
		if strings.TrimSpace(body) == "" {
			return "", "", errors.New("Agent Skill prompt body is required")
		}
		return strings.Join(lines[1:i], "\n"), body, nil
	}
	return "", "", errors.New("Agent Skill frontmatter is missing a closing --- line")
}

func validateAgentSkillMetadata(skill AgentSkill) error {
	if skill.Name == "" {
		return errors.New("Agent Skill name is required")
	}
	if len(skill.Name) > MaxAgentSkillNameBytes || !agentSkillNamePattern.MatchString(skill.Name) {
		return fmt.Errorf("Agent Skill name must be at most %d bytes of lowercase letters, numbers, and single hyphens", MaxAgentSkillNameBytes)
	}
	if skill.Description == "" {
		return errors.New("Agent Skill description is required")
	}
	if len(skill.Description) > MaxAgentSkillDescriptionBytes {
		return fmt.Errorf("Agent Skill description exceeds %d bytes", MaxAgentSkillDescriptionBytes)
	}
	if len(skill.Compatibility) > MaxAgentSkillCompatibilityBytes {
		return fmt.Errorf("Agent Skill compatibility exceeds %d bytes", MaxAgentSkillCompatibilityBytes)
	}
	for _, value := range []string{skill.Name, skill.Description, skill.License, skill.Compatibility, skill.Prompt} {
		if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return errors.New("Agent Skill fields must be valid UTF-8 without NUL bytes")
		}
	}
	return nil
}

func parseAgentSkillMetadata(node *yaml.Node) (map[string]string, error) {
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" {
		return nil, errors.New("must be a string-to-string mapping")
	}
	if len(node.Content)/2 > MaxAgentSkillMetadataEntries {
		return nil, fmt.Errorf("exceeds %d entries", MaxAgentSkillMetadataEntries)
	}
	result := make(map[string]string, len(node.Content)/2)
	for i := 0; i < len(node.Content); i += 2 {
		key, err := strictYAMLString(node.Content[i])
		if err != nil {
			return nil, errors.New("keys must be strings")
		}
		value, err := strictYAMLString(node.Content[i+1])
		if err != nil {
			return nil, fmt.Errorf("value for %q must be a string", key)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || len(key) > MaxAgentSkillMetadataKeyBytes {
			return nil, fmt.Errorf("metadata key must be 1-%d bytes", MaxAgentSkillMetadataKeyBytes)
		}
		if len(value) > MaxAgentSkillMetadataValueBytes {
			return nil, fmt.Errorf("metadata value for %q exceeds %d bytes", key, MaxAgentSkillMetadataValueBytes)
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate metadata key %q", key)
		}
		result[key] = value
	}
	return result, nil
}

func decodeStrictYAMLMapping(content string) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple YAML documents are not allowed")
		}
		return nil, err
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, errors.New("frontmatter must be one YAML document")
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode || root.Tag != "!!map" {
		return nil, errors.New("frontmatter must be a mapping")
	}
	if err := rejectUnsafeYAMLNode(root); err != nil {
		return nil, err
	}
	return root, nil
}

func rejectUnsafeYAMLNode(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("YAML aliases and anchors are not allowed")
	}
	switch node.Kind {
	case yaml.MappingNode:
		if node.Tag != "!!map" {
			return errors.New("custom YAML mapping tags are not allowed")
		}
	case yaml.SequenceNode:
		if node.Tag != "!!seq" {
			return errors.New("custom YAML sequence tags are not allowed")
		}
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return errors.New("frontmatter scalar values must be explicit strings")
		}
	}
	for _, child := range node.Content {
		if err := rejectUnsafeYAMLNode(child); err != nil {
			return err
		}
	}
	return nil
}

func strictYAMLString(node *yaml.Node) (string, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || node.Anchor != "" {
		return "", errors.New("must be a string")
	}
	if !utf8.ValidString(node.Value) || strings.ContainsRune(node.Value, 0) {
		return "", errors.New("must be valid UTF-8 without NUL bytes")
	}
	return node.Value, nil
}
