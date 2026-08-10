package config

import "testing"

// A pasted token must never survive normalization. Storing one would write
// credential material into config.json, which is exactly what the env:
// reference form exists to prevent.
func TestNamedTunnelRejectsPlaintextTokenReference(t *testing.T) {
	for _, tokenRef := range []string{
		"eyJhIjoiYiIsInMiOiJjIn0",              // a token-shaped value
		"plaintext-secret",                     // no scheme
		"file:/etc/cloudflared/creds.json",     // unsupported scheme
		"env:has space",                        // invalid variable name
		"env:",                                 // empty name
		"vault:AUTOTO_CLOUDFLARE_TUNNEL_TOKEN", // unsupported scheme
	} {
		got := normalizeNamedTunnelConfig(NamedTunnelConfig{
			Hostname: "autoto.example.com",
			TokenRef: tokenRef,
		})
		if got.TokenRef != "" {
			t.Fatalf("token reference %q was accepted as %q", tokenRef, got.TokenRef)
		}
		if got.Configured() {
			t.Fatalf("token reference %q left the tunnel configured", tokenRef)
		}
	}
}

func TestNamedTunnelAcceptsEnvReferenceAndNormalizesHostname(t *testing.T) {
	got := normalizeNamedTunnelConfig(NamedTunnelConfig{
		Hostname: "  Autoto.Example.COM  ",
		TokenRef: "  env:AUTOTO_CLOUDFLARE_TUNNEL_TOKEN  ",
	})
	if got.Hostname != "autoto.example.com" {
		t.Fatalf("unexpected hostname: %q", got.Hostname)
	}
	if got.TokenRef != "env:AUTOTO_CLOUDFLARE_TUNNEL_TOKEN" {
		t.Fatalf("unexpected token reference: %q", got.TokenRef)
	}
	if !got.Configured() {
		t.Fatal("expected the tunnel to be configured")
	}
}

// A hostname carrying a scheme, port, or path would produce a public URL that
// looks correct and is not, so those are rejected rather than repaired.
func TestNamedTunnelRejectsUnusableHostnames(t *testing.T) {
	for _, hostname := range []string{
		"https://autoto.example.com",
		"autoto.example.com:8443",
		"autoto.example.com/path",
		"user@autoto.example.com",
		"localhost",
		"autoto",
		".example.com",
		"example.com.",
		"-bad.example.com",
		"bad-.example.com",
		"autoto..example.com",
		"autoto.example.com\\x",
	} {
		got := normalizeNamedTunnelConfig(NamedTunnelConfig{
			Hostname: hostname,
			TokenRef: "env:AUTOTO_CLOUDFLARE_TUNNEL_TOKEN",
		})
		if got.Hostname != "" {
			t.Fatalf("hostname %q was accepted as %q", hostname, got.Hostname)
		}
		if got.Configured() {
			t.Fatalf("hostname %q left the tunnel configured", hostname)
		}
	}
}

// AutoStart must not survive on an unusable tunnel. Leaving it set would arm the
// tunnel the moment the remaining fields were filled in, which is a change in
// exposure the user never confirmed.
func TestNamedTunnelDropsAutoStartWhenNotConfigured(t *testing.T) {
	got := normalizeNamedTunnelConfig(NamedTunnelConfig{
		Hostname:  "autoto.example.com",
		TokenRef:  "plaintext-secret",
		AutoStart: true,
	})
	if got.AutoStart {
		t.Fatal("auto-start survived an unusable token reference")
	}

	got = normalizeNamedTunnelConfig(NamedTunnelConfig{AutoStart: true})
	if got.AutoStart {
		t.Fatal("auto-start survived an empty configuration")
	}
}

// Both tunnels must normalize, or one of them could carry a plaintext token.
func TestNamedTunnelNormalizesOnBothSecurityAndGateway(t *testing.T) {
	security := normalizeSecurityConfig(SecurityConfig{
		NamedTunnel: NamedTunnelConfig{Hostname: "ui.example.com", TokenRef: "plaintext"},
	})
	if security.NamedTunnel.TokenRef != "" {
		t.Fatalf("security tunnel kept a plaintext token: %q", security.NamedTunnel.TokenRef)
	}

	gateway := normalizeGatewayConfig(GatewayConfig{
		NamedTunnel: NamedTunnelConfig{Hostname: "api.example.com", TokenRef: "plaintext"},
	})
	if gateway.NamedTunnel.TokenRef != "" {
		t.Fatalf("gateway tunnel kept a plaintext token: %q", gateway.NamedTunnel.TokenRef)
	}
}
