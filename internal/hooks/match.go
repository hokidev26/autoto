package hooks

import "path"

func Matches(hook Hook, event Event) bool {
	if !hook.Enabled || hook.Event != event.Name || !scopeMatches(hook.Scope, event) {
		return false
	}
	filter := hook.Filter
	return matchesAny(filter.ProjectIDs, event.ProjectID) &&
		matchesAny(filter.AgentIDs, event.AgentID) &&
		matchesAny(filter.ToolNames, event.ToolName) &&
		matchesAny(filter.RunKinds, event.RunKind) &&
		attributesMatch(filter.Attributes, event.Attributes)
}

func scopeMatches(scope Scope, event Event) bool {
	switch scope.Kind {
	case ScopeGlobal:
		return true
	case ScopeProject:
		return scope.ID == event.ProjectID
	case ScopeAgent:
		return scope.ID == event.AgentID
	default:
		return false
	}
}

func matchesAny(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if pattern == value {
			return true
		}
		if matched, err := path.Match(pattern, value); err == nil && matched {
			return true
		}
	}
	return false
}

func attributesMatch(filters map[string][]string, values map[string]string) bool {
	for key, patterns := range filters {
		if !matchesAny(patterns, values[key]) {
			return false
		}
	}
	return true
}
