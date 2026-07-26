package peercontrol

import (
	"errors"
	"strings"
)

// Scope is a normalized remote peer capability.
type Scope string

const (
	ScopeObserve      Scope = "observe"
	ScopeSendTask     Scope = "send_task"
	ScopeApproveOnce  Scope = "approve_once"
	ScopeExecuteTools Scope = "execute_tools"
)

var scopeOrder = []Scope{
	ScopeObserve,
	ScopeSendTask,
	ScopeApproveOnce,
	ScopeExecuteTools,
}

// AllScopes returns all recognized scopes in canonical order.
func AllScopes() []Scope {
	return append([]Scope(nil), scopeOrder...)
}

// NormalizeScope trims and lowercases a single recognized scope.
func NormalizeScope(value string) (Scope, error) {
	normalized := Scope(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case ScopeObserve, ScopeSendTask, ScopeApproveOnce, ScopeExecuteTools:
		return normalized, nil
	default:
		return "", errors.New("unknown peer scope")
	}
}

// NormalizeScopes validates, de-duplicates, and returns scopes in canonical
// order so persisted and signed representations remain stable.
func NormalizeScopes(values []string) ([]Scope, error) {
	selected := make(map[Scope]struct{}, len(values))
	for _, value := range values {
		scope, err := NormalizeScope(value)
		if err != nil {
			return nil, err
		}
		selected[scope] = struct{}{}
	}
	normalized := make([]Scope, 0, len(selected))
	for _, scope := range scopeOrder {
		if _, ok := selected[scope]; ok {
			normalized = append(normalized, scope)
		}
	}
	return normalized, nil
}
