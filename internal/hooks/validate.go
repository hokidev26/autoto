package hooks

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxHookNameBytes       = 120
	MaxDescriptionBytes    = 2000
	MaxFilterEntries       = 128
	MaxActionPayloadBytes  = 64 << 10
	DefaultTimeoutSeconds  = 30
	MaximumTimeoutSeconds  = 300
	DefaultMaxOutputTokens = 256
	MaximumMaxOutputTokens = 4096
)

func NormalizeAndValidateHook(input Hook) (Hook, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Scope.ID = strings.TrimSpace(input.Scope.ID)
	if input.Mode == "" {
		input.Mode = ModeSync
	}
	if input.FailurePolicy == "" {
		input.FailurePolicy = FailureContinue
	}
	if input.Action.HTTP != nil {
		input.Action.HTTP.Method = strings.ToUpper(strings.TrimSpace(input.Action.HTTP.Method))
		if input.Action.HTTP.Method == "" {
			input.Action.HTTP.Method = http.MethodPost
		}
		if input.Action.HTTP.TimeoutSeconds == 0 {
			input.Action.HTTP.TimeoutSeconds = DefaultTimeoutSeconds
		}
	}
	if input.Action.Shell != nil {
		input.Action.Shell.Executable = strings.TrimSpace(input.Action.Shell.Executable)
		input.Action.Shell.CWD = strings.TrimSpace(input.Action.Shell.CWD)
		if input.Action.Shell.TimeoutSeconds == 0 {
			input.Action.Shell.TimeoutSeconds = DefaultTimeoutSeconds
		}
	}
	if input.Action.LLM != nil {
		input.Action.LLM.Model = strings.TrimSpace(input.Action.LLM.Model)
		input.Action.LLM.Prompt = strings.TrimSpace(input.Action.LLM.Prompt)
		if input.Action.LLM.MaxOutputTokens == 0 {
			input.Action.LLM.MaxOutputTokens = DefaultMaxOutputTokens
		}
		if input.Action.LLM.TimeoutSeconds == 0 {
			input.Action.LLM.TimeoutSeconds = DefaultTimeoutSeconds
		}
	}
	normalizeFilter(&input.Filter)
	if err := ValidateHook(input); err != nil {
		return Hook{}, err
	}
	return input, nil
}

func ValidateHook(hook Hook) error {
	if hook.ID != "" && !validIdentifier(hook.ID, 128) {
		return errors.New("invalid hook id")
	}
	if err := validText(hook.Name, MaxHookNameBytes, false, "hook name"); err != nil {
		return err
	}
	if err := validText(hook.Description, MaxDescriptionBytes, true, "hook description"); err != nil {
		return err
	}
	switch hook.Event {
	case EventRunBefore, EventRunAfter, EventToolBefore, EventToolAfter:
	default:
		return errors.New("invalid hook event")
	}
	switch hook.Scope.Kind {
	case ScopeGlobal:
		if hook.Scope.ID != "" {
			return errors.New("global hook scope must not have an id")
		}
	case ScopeProject, ScopeAgent:
		if !validIdentifier(hook.Scope.ID, 128) {
			return errors.New("scoped hook requires a valid id")
		}
	default:
		return errors.New("invalid hook scope")
	}
	if hook.Priority < -1000 || hook.Priority > 1000 {
		return errors.New("hook priority must be between -1000 and 1000")
	}
	if hook.Revision < 0 {
		return errors.New("hook revision must not be negative")
	}
	if err := validateFilter(hook.Event, hook.Filter); err != nil {
		return err
	}
	switch hook.Mode {
	case ModeSync, ModeAsync:
	default:
		return errors.New("invalid hook dispatch mode")
	}
	if (hook.Event == EventRunBefore || hook.Event == EventToolBefore) && hook.Mode != ModeSync {
		return errors.New("before hooks must be synchronous")
	}
	switch hook.FailurePolicy {
	case FailureContinue, FailureFailRun, FailureRetry, FailureDisableHook:
	default:
		return errors.New("invalid hook failure policy")
	}
	if hook.Mode == ModeAsync && hook.FailurePolicy == FailureFailRun {
		return errors.New("asynchronous hooks cannot fail a run")
	}
	if err := ValidateAction(hook.Action); err != nil {
		return err
	}
	if hook.Action.Kind == ActionLLM && (hook.Event != EventRunBefore && hook.Event != EventToolBefore || hook.Mode != ModeSync) {
		return errors.New("LLM gates are only valid as synchronous before hooks")
	}
	return nil
}

func ValidateAction(action Action) error {
	configured := 0
	if action.Shell != nil {
		configured++
	}
	if action.HTTP != nil {
		configured++
	}
	if action.LLM != nil {
		configured++
	}
	if configured != 1 {
		return errors.New("hook action must configure exactly one action")
	}
	switch action.Kind {
	case ActionShell:
		if action.Shell == nil {
			return errors.New("shell action configuration is required")
		}
		return validateShellAction(*action.Shell)
	case ActionHTTP:
		if action.HTTP == nil {
			return errors.New("HTTP action configuration is required")
		}
		return validateHTTPAction(*action.HTTP)
	case ActionLLM:
		if action.LLM == nil {
			return errors.New("LLM action configuration is required")
		}
		return validateLLMAction(*action.LLM)
	default:
		return errors.New("invalid hook action kind")
	}
}

func validateShellAction(action ShellAction) error {
	if !action.CanonicalStdinV1 {
		return errors.New("shell actions must consume canonical event stdin v1")
	}
	if !validCommandName(action.Executable) {
		return errors.New("invalid shell executable")
	}
	if action.Detached {
		return errors.New("shell actions cannot run detached or in the background")
	}
	if action.CWD != "" {
		if filepath.IsAbs(action.CWD) || filepath.VolumeName(action.CWD) != "" || strings.HasPrefix(action.CWD, "/") || strings.HasPrefix(action.CWD, `\`) {
			return errors.New("shell action cwd must be workspace-relative")
		}
		cleaned := filepath.Clean(action.CWD)
		if cleaned == ".." || strings.HasPrefix(cleaned, `..`+string(filepath.Separator)) {
			return errors.New("shell action cwd cannot escape the workspace")
		}
	}
	for _, arg := range action.Args {
		if !utf8.ValidString(arg) || strings.ContainsAny(arg, "\x00\r\n") {
			return errors.New("invalid shell argument")
		}
		lower := strings.ToLower(strings.TrimSpace(arg))
		if lower == "&" || lower == "nohup" || lower == "setsid" || lower == "start" {
			return errors.New("shell action contains a background escape")
		}
	}
	if err := validateEnv(action.Env, false); err != nil {
		return err
	}
	if err := validateEnv(action.SecretRefs, true); err != nil {
		return err
	}
	return validateTimeout(action.TimeoutSeconds)
}

func validateHTTPAction(action HTTPAction) error {
	parsed, err := url.Parse(strings.TrimSpace(action.URL))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("invalid HTTP action URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
	case "http":
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			return errors.New("HTTP action requires HTTPS except for loopback targets")
		}
	default:
		return errors.New("HTTP action URL has an unsupported protocol")
	}
	method := strings.ToUpper(strings.TrimSpace(action.Method))
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return errors.New("HTTP action method must be POST, PUT, or PATCH")
	}
	for name, value := range action.Headers {
		if !validHeaderName(name) || !validHeaderValue(value) {
			return errors.New("invalid HTTP action header")
		}
		if sensitiveHeader(name) {
			return errors.New("sensitive HTTP headers must use secretRefs")
		}
	}
	for name, ref := range action.SecretRefs {
		if !validHeaderName(name) || !sensitiveHeader(name) && !strings.HasPrefix(strings.ToLower(name), "x-") {
			return errors.New("invalid HTTP secret header")
		}
		if !validSecretRef(ref) {
			return errors.New("HTTP action secrets must be references")
		}
	}
	return validateTimeout(action.TimeoutSeconds)
}

func validateLLMAction(action LLMAction) error {
	if !validIdentifier(action.Model, 256) {
		return errors.New("invalid LLM gateway model")
	}
	if err := validText(action.Prompt, 16<<10, false, "LLM gate prompt"); err != nil {
		return err
	}
	if action.MaxOutputTokens < 1 || action.MaxOutputTokens > MaximumMaxOutputTokens {
		return fmt.Errorf("LLM gate max output tokens must be between 1 and %d", MaximumMaxOutputTokens)
	}
	return validateTimeout(action.TimeoutSeconds)
}

func validateTimeout(seconds int) error {
	if seconds < 1 || seconds > MaximumTimeoutSeconds {
		return fmt.Errorf("action timeout must be between 1 and %d seconds", MaximumTimeoutSeconds)
	}
	return nil
}

func validateFilter(event EventName, filter Filter) error {
	count := len(filter.ProjectIDs) + len(filter.AgentIDs) + len(filter.ToolNames) + len(filter.RunKinds)
	for key, values := range filter.Attributes {
		if !validIdentifier(key, 64) || len(values) == 0 {
			return errors.New("invalid hook attribute filter")
		}
		count += len(values)
	}
	if count > MaxFilterEntries {
		return fmt.Errorf("hook filter exceeds %d entries", MaxFilterEntries)
	}
	for _, values := range [][]string{filter.ProjectIDs, filter.AgentIDs, filter.ToolNames, filter.RunKinds} {
		for _, value := range values {
			if !validPattern(value) {
				return errors.New("invalid hook filter pattern")
			}
		}
	}
	for _, values := range filter.Attributes {
		for _, value := range values {
			if !validPattern(value) {
				return errors.New("invalid hook attribute filter pattern")
			}
		}
	}
	if len(filter.ToolNames) > 0 && event != EventToolBefore && event != EventToolAfter {
		return errors.New("toolName filters are only valid for tool events")
	}
	return nil
}

func normalizeFilter(filter *Filter) {
	filter.ProjectIDs = normalizeList(filter.ProjectIDs)
	filter.AgentIDs = normalizeList(filter.AgentIDs)
	filter.ToolNames = normalizeList(filter.ToolNames)
	filter.RunKinds = normalizeList(filter.RunKinds)
	if len(filter.Attributes) == 0 {
		filter.Attributes = nil
		return
	}
	normalized := make(map[string][]string, len(filter.Attributes))
	for key, values := range filter.Attributes {
		normalized[strings.TrimSpace(key)] = normalizeList(values)
	}
	filter.Attributes = normalized
}

func normalizeList(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			unique[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validateEnv(values map[string]string, secret bool) error {
	if len(values) > 64 {
		return errors.New("action environment exceeds 64 entries")
	}
	for name, value := range values {
		if !validEnvName(name) {
			return errors.New("invalid action environment name")
		}
		if secret {
			if !validSecretRef(value) {
				return errors.New("action secrets must be references")
			}
		} else if !validHeaderValue(value) {
			return errors.New("invalid action environment value")
		}
	}
	return nil
}

func validSecretRef(value string) bool {
	value = strings.TrimSpace(value)
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || parts[1] == "" {
		return false
	}
	switch parts[0] {
	case "env", "secret", "vault", "keychain":
	default:
		return false
	}
	return validIdentifier(parts[1], 512)
}

func validIdentifier(value string, max int) bool {
	return validText(value, max, false, "value") == nil && value == strings.TrimSpace(value)
}
func validText(value string, max int, empty bool, name string) error {
	if !utf8.ValidString(value) || len(value) > max || (!empty && value == "") {
		return fmt.Errorf("invalid %s", name)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("invalid %s", name)
		}
	}
	return nil
}
func validPattern(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}
func validEnvName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i, r := range value {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func validCommandName(value string) bool {
	if !validIdentifier(value, 128) || strings.ContainsAny(value, `/\`) {
		return false
	}
	switch strings.ToLower(value) {
	case "sh", "bash", "zsh", "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "nohup", "setsid":
		return false
	}
	return true
}
func validHeaderName(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n:")
}
func validHeaderValue(value string) bool {
	return utf8.ValidString(value) && len(value) <= 4096 && !strings.ContainsAny(value, "\x00\r\n")
}
func sensitiveHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key", "api-key":
		return true
	}
	return strings.Contains(strings.ToLower(name), "token") || strings.Contains(strings.ToLower(name), "secret")
}
