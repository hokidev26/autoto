package agentprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const MaxDefinitionBytes = 64 << 10

// ParseRoleDefinition strictly parses a versioned role preset. Unknown fields,
// trailing JSON values, invalid UTF-8, and oversized payloads are rejected.
func ParseRoleDefinition(data []byte) (RoleDefinition, error) {
	if len(data) == 0 {
		return RoleDefinition{}, errors.New("role definition is required")
	}
	if len(data) > MaxDefinitionBytes {
		return RoleDefinition{}, fmt.Errorf("role definition exceeds %d bytes", MaxDefinitionBytes)
	}
	if !utf8.Valid(data) {
		return RoleDefinition{}, errors.New("role definition must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var definition RoleDefinition
	if err := decoder.Decode(&definition); err != nil {
		return RoleDefinition{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return RoleDefinition{}, errors.New("role definition must contain exactly one JSON value")
		}
		return RoleDefinition{}, err
	}
	if err := definition.Validate(); err != nil {
		return RoleDefinition{}, err
	}
	normalizedAllow, _ := normalizeToolNames(definition.ToolAllowlist)
	normalizedDeny, _ := normalizeToolNames(definition.DeniedTools)
	definition.ToolAllowlist = normalizedAllow
	definition.DeniedTools = normalizedDeny
	return definition, nil
}
