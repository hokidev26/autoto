package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"autoto/internal/config"
	"autoto/internal/secrets"
)

// namedTunnelRequest configures a named tunnel. The token is supplied as an
// env:VARIABLE_NAME reference rather than a value, matching the integration
// connections: a token pasted into this endpoint would end up in config.json,
// in request logs, and in any backup of either.
type namedTunnelRequest struct {
	Hostname  string `json:"hostname"`
	TokenRef  string `json:"tokenRef"`
	AutoStart *bool  `json:"autoStart"`
}

// namedTunnelResponse never carries the token or its resolved value. It reports
// the reference so the user can see which variable is in use, plus whether that
// variable currently resolves, which is the part they actually need to debug a
// tunnel that will not start.
type namedTunnelResponse struct {
	Hostname       string `json:"hostname,omitempty"`
	TokenRef       string `json:"tokenRef,omitempty"`
	AutoStart      bool   `json:"autoStart"`
	Configured     bool   `json:"configured"`
	TokenAvailable bool   `json:"tokenAvailable"`
}

func namedTunnelSnapshot(ctx context.Context, tunnel config.NamedTunnelConfig) namedTunnelResponse {
	response := namedTunnelResponse{
		Hostname:   tunnel.Hostname,
		TokenRef:   tunnel.TokenRef,
		AutoStart:  tunnel.AutoStart,
		Configured: tunnel.Configured(),
	}
	if tunnel.TokenRef != "" {
		// Only presence is reported. The resolved value is discarded immediately and
		// never reaches the response, a log, or an error message.
		if _, err := secrets.ResolveString(ctx, secrets.EnvResolver{}, tunnel.TokenRef); err == nil {
			response.TokenAvailable = true
		}
	}
	return response
}

func (s *Server) getNamedTunnelSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.configSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"remoteAccess": namedTunnelSnapshot(r.Context(), cfg.Security.NamedTunnel),
		"sharedApi":    namedTunnelSnapshot(r.Context(), cfg.Gateway.NamedTunnel),
	})
}

func (s *Server) updateRemoteAccessNamedTunnel(w http.ResponseWriter, r *http.Request) {
	s.updateNamedTunnel(w, r, func(cfg *config.Config, tunnel config.NamedTunnelConfig) {
		cfg.Security.NamedTunnel = tunnel
	}, func(cfg config.Config) config.NamedTunnelConfig {
		return cfg.Security.NamedTunnel
	})
}

func (s *Server) updateSharedAPINamedTunnel(w http.ResponseWriter, r *http.Request) {
	s.updateNamedTunnel(w, r, func(cfg *config.Config, tunnel config.NamedTunnelConfig) {
		cfg.Gateway.NamedTunnel = tunnel
	}, func(cfg config.Config) config.NamedTunnelConfig {
		return cfg.Gateway.NamedTunnel
	})
}

// updateNamedTunnel persists one named tunnel section.
//
// It reuses remoteSecurityMutationAllowed for the same reason the tunnel
// start/stop endpoints do: this changes how the machine is reachable from the
// internet, so it is a local-only operation and must not be driveable from a
// remote session.
func (s *Server) updateNamedTunnel(
	w http.ResponseWriter,
	r *http.Request,
	apply func(*config.Config, config.NamedTunnelConfig),
	read func(config.Config) config.NamedTunnelConfig,
) {
	var req namedTunnelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	if ok, message := s.remoteSecurityMutationAllowed(r, ""); !ok {
		writeError(w, http.StatusForbidden, message)
		return
	}

	tunnel, err := validateNamedTunnelRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A tunnel that starts with Autoto keeps a stable hostname, so it makes this
	// machine reachable from boot with the access password as the only barrier.
	// Refusing to arm that without a password matches the manual start path,
	// which already refuses for the same reason.
	if tunnel.AutoStart {
		if configured, _ := s.credentialConfigured(); !configured {
			writeError(w, http.StatusConflict, "configure an access password before enabling named tunnel auto-start")
			return
		}
	}

	path := s.configPathSnapshot()
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusServiceUnavailable, "security configuration path is unavailable")
		return
	}
	updated := s.configSnapshot()
	apply(&updated, tunnel)
	if err := config.Save(path, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "named tunnel settings were not saved: "+err.Error())
		return
	}
	// Apply the same normalization the config layer applies, so what is reported
	// back and held in memory is what a later start will actually use rather than
	// what was sent.
	normalized := updated
	apply(&normalized, config.NormalizeNamedTunnel(tunnel))
	s.cfgMu.Lock()
	s.cfg = normalized
	s.cfgMu.Unlock()
	writeJSON(w, http.StatusOK, namedTunnelSnapshot(r.Context(), read(normalized)))
}

// validateNamedTunnelRequest rejects input rather than repairing it. A silently
// corrected hostname would publish traffic somewhere the user did not ask for,
// and a silently dropped token reference would leave a tunnel that cannot start
// with no explanation.
func validateNamedTunnelRequest(req namedTunnelRequest) (config.NamedTunnelConfig, error) {
	hostname := strings.ToLower(strings.TrimSpace(req.Hostname))
	tokenRef := strings.TrimSpace(req.TokenRef)
	autoStart := req.AutoStart != nil && *req.AutoStart

	// Both empty clears the section, which is how a named tunnel is turned off.
	if hostname == "" && tokenRef == "" {
		return config.NamedTunnelConfig{}, nil
	}
	if hostname == "" {
		return config.NamedTunnelConfig{}, errors.New("hostname is required")
	}
	if tokenRef == "" {
		return config.NamedTunnelConfig{}, errors.New("tokenRef is required")
	}
	if _, err := secrets.ParseRef(tokenRef); err != nil {
		return config.NamedTunnelConfig{}, errors.New("tokenRef must be an environment reference in the form env:VARIABLE_NAME")
	}
	candidate := config.NamedTunnelConfig{Hostname: hostname, TokenRef: tokenRef, AutoStart: autoStart}
	// Normalization is the authority on what is acceptable. Checking the result
	// here keeps this endpoint from accepting something the config layer would
	// discard, which would look like a successful save that did nothing.
	if normalized := config.NormalizeNamedTunnel(candidate); !normalized.Configured() {
		return config.NamedTunnelConfig{}, errors.New("hostname must be a fully qualified domain name such as autoto.example.com")
	}
	return candidate, nil
}
