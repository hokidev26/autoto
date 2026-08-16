package agentprofile

import "strings"

// Trust identifies both instruction authority and the transport role used for
// a prompt layer. User-context layers are never promoted into system text.
type Trust string

const (
	TrustImmutableSystem Trust = "immutable_system"
	TrustSystemExtension Trust = "system_extension"
	TrustUntrustedUser   Trust = "untrusted_user"
)

type PromptLayer struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	Trust     Trust  `json:"trust"`
	Content   string `json:"content"`
	Immutable bool   `json:"immutable"`
}

type ComposeInput struct {
	Platform        string
	Run             string
	Role            string
	HostRuntime     string
	SystemExtension string
	RoleExtension   string
	LegacyPersona   string
	GlobalUser      string
	ProjectContext  string
	MemoryContext   string
	ClosingBoundary string
}

// Compose returns prompt layers in their fixed trust order. Platform, run,
// canonical role, host runtime, and closing boundary are immutable. global_user
// is emitted as explicit untrusted user context, never as a system layer.
func Compose(input ComposeInput) []PromptLayer {
	layers := make([]PromptLayer, 0, 11)
	appendLayer := func(name, role string, trust Trust, immutable bool, content string) {
		content = strings.TrimSpace(content)
		if content == "" {
			return
		}
		layers = append(layers, PromptLayer{Name: name, Role: role, Trust: trust, Content: content, Immutable: immutable})
	}
	appendLayer("platform", "system", TrustImmutableSystem, true, input.Platform)
	appendLayer("run", "system", TrustImmutableSystem, true, input.Run)
	appendLayer("role", "system", TrustImmutableSystem, true, input.Role)
	appendLayer("host_runtime", "system", TrustImmutableSystem, true, input.HostRuntime)
	appendLayer("system_extension", "system", TrustSystemExtension, false, input.SystemExtension)
	appendLayer("role_extension", "system", TrustSystemExtension, false, input.RoleExtension)
	appendLayer("legacy_persona", "user", TrustUntrustedUser, false, wrapContext("legacy_persona", input.LegacyPersona))
	appendLayer("global_user", "user", TrustUntrustedUser, false, wrapContext("global_user", input.GlobalUser))
	appendLayer("project", "user", TrustUntrustedUser, false, wrapContext("project", input.ProjectContext))
	appendLayer("memory", "user", TrustUntrustedUser, false, wrapContext("memory", input.MemoryContext))
	appendLayer("closing_boundary", "system", TrustImmutableSystem, true, input.ClosingBoundary)
	return layers
}

func wrapContext(name, content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	return "<untrusted_context source=\"" + name + "\">\n" + content + "\n</untrusted_context>"
}

// RenderSystem joins only system-authority layers. It intentionally excludes
// legacy persona, global_user, project, and memory context.
func RenderSystem(layers []PromptLayer) string {
	parts := make([]string, 0, len(layers))
	for _, layer := range layers {
		if layer.Role == "system" && strings.TrimSpace(layer.Content) != "" {
			parts = append(parts, layer.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}
