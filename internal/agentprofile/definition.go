// Package agentprofile defines custom child-agent presets and prompt composition
// without weakening the platform's canonical role contracts.
package agentprofile

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"autoto/internal/agentrole"
)

const DefinitionVersion = 1

// RoleDefinition is the persisted, user-configurable portion of a child-agent
// preset. BaseRole selects an immutable canonical contract; RoleExtension may
// add instructions but cannot replace that contract.
type RoleDefinition struct {
	Version       int      `json:"version"`
	Key           string   `json:"key"`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description,omitempty"`
	BaseRole      string   `json:"baseRole"`
	RoleExtension string   `json:"roleExtension,omitempty"`
	ToolAllowlist []string `json:"toolAllowlist,omitempty"`
	DeniedTools   []string `json:"deniedTools,omitempty"`
	ReadOnly      bool     `json:"readOnly,omitempty"`
	DisableExec   bool     `json:"disableExec,omitempty"`
}

// CapabilitySet is the parent's already-authorized capability ceiling.
// Resolving a custom role can only remove entries from this set.
type CapabilitySet struct {
	Tools         map[string]bool
	WritableTools map[string]bool
	ExecTools     map[string]bool
}

// ResolvedRole keeps the immutable canonical prompt separate from the custom
// extension so callers cannot accidentally treat the extension as a base-role
// replacement.
type ResolvedRole struct {
	Definition          RoleDefinition
	Contract            agentrole.Contract
	ImmutableRolePrompt string
	RoleExtension       string
	Capabilities        CapabilitySet
}

func (d RoleDefinition) Validate() error {
	if d.Version != DefinitionVersion {
		return fmt.Errorf("version must be %d", DefinitionVersion)
	}
	if !validKey(d.Key) {
		return errors.New("key must be 1-64 lowercase letters, digits, dots, underscores, or hyphens")
	}
	if err := validateText("displayName", d.DisplayName, 120, true, false); err != nil {
		return err
	}
	if err := validateText("description", d.Description, 500, false, false); err != nil {
		return err
	}
	if err := validateText("roleExtension", d.RoleExtension, 16<<10, false, true); err != nil {
		return err
	}
	contract, err := agentrole.Resolve(d.BaseRole)
	if err != nil {
		return fmt.Errorf("baseRole: %w", err)
	}
	if contract.Role == "" {
		return errors.New("baseRole is required")
	}
	allow, err := normalizeToolNames(d.ToolAllowlist)
	if err != nil {
		return fmt.Errorf("toolAllowlist: %w", err)
	}
	deny, err := normalizeToolNames(d.DeniedTools)
	if err != nil {
		return fmt.Errorf("deniedTools: %w", err)
	}
	denied := make(map[string]bool, len(deny))
	for _, name := range deny {
		denied[strings.ToLower(name)] = true
	}
	for _, name := range allow {
		if denied[strings.ToLower(name)] {
			return fmt.Errorf("tool %q cannot be both allowed and denied", name)
		}
	}
	return nil
}

// Resolve applies the definition to a parent capability ceiling. It rejects an
// allowlist that names a tool unavailable to the parent instead of silently
// broadening or inventing authority.
func (d RoleDefinition) Resolve(parent CapabilitySet) (ResolvedRole, error) {
	if err := d.Validate(); err != nil {
		return ResolvedRole{}, err
	}
	contract, err := agentrole.Resolve(d.BaseRole)
	if err != nil {
		return ResolvedRole{}, err
	}
	tools := copySet(parent.Tools)
	if len(d.ToolAllowlist) > 0 {
		allowed := make(map[string]bool, len(d.ToolAllowlist))
		for _, requested := range d.ToolAllowlist {
			actual, ok := lookupTool(tools, requested)
			if !ok {
				return ResolvedRole{}, fmt.Errorf("toolAllowlist requests unavailable parent tool %q", requested)
			}
			allowed[actual] = true
		}
		tools = allowed
	}
	for _, denied := range d.DeniedTools {
		if actual, ok := lookupTool(tools, denied); ok {
			delete(tools, actual)
		}
	}
	readOnly := d.ReadOnly || contract.ReadOnly
	for name := range tools {
		if readOnly && (setContains(parent.WritableTools, name) || setContains(parent.ExecTools, name)) {
			delete(tools, name)
			continue
		}
		if d.DisableExec && setContains(parent.ExecTools, name) {
			delete(tools, name)
		}
	}
	return ResolvedRole{
		Definition:          d,
		Contract:            contract,
		ImmutableRolePrompt: contract.Prompt,
		RoleExtension:       strings.TrimSpace(d.RoleExtension),
		Capabilities: CapabilitySet{
			Tools:         tools,
			WritableTools: intersectSets(parent.WritableTools, tools),
			ExecTools:     intersectSets(parent.ExecTools, tools),
		},
	}, nil
}

func validKey(value string) bool {
	if len(value) < 1 || len(value) > 64 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || strings.ContainsRune("._-", char) {
			continue
		}
		return false
	}
	return true
}

func normalizeToolNames(values []string) ([]string, error) {
	if len(values) > 128 {
		return nil, errors.New("exceeds 128 entries")
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("contains an invalid tool name")
		}
		key := strings.ToLower(value)
		if seen[key] {
			return nil, fmt.Errorf("contains duplicate tool %q", value)
		}
		seen[key] = true
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result, nil
}

func validateText(name, value string, maxBytes int, required, multiline bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len([]byte(value)) > maxBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxBytes)
	}
	if strings.ContainsRune(value, '\x00') || (!multiline && strings.ContainsAny(value, "\r\n")) {
		return fmt.Errorf("%s contains invalid control characters", name)
	}
	return nil
}

func copySet(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for key, value := range source {
		if value {
			result[key] = true
		}
	}
	return result
}

func lookupTool(tools map[string]bool, requested string) (string, bool) {
	for name, enabled := range tools {
		if enabled && strings.EqualFold(name, requested) {
			return name, true
		}
	}
	return "", false
}

func setContains(values map[string]bool, requested string) bool {
	_, ok := lookupTool(values, requested)
	return ok
}

func intersectSets(left, right map[string]bool) map[string]bool {
	result := map[string]bool{}
	for name := range left {
		if actual, ok := lookupTool(right, name); ok {
			result[actual] = true
		}
	}
	return result
}
