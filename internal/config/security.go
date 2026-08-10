package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"autoto/internal/secrets"
)

const accessPasswordHashV1 = "sha256-bcrypt-v1$"

// HashAccessPassword creates the durable password representation. The password
// is first reduced with SHA-256, then bcrypt is applied with its per-hash salt.
// The version prefix keeps room for future credential migrations.
func HashAccessPassword(password string) (string, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return "", errors.New("access password is required")
	}
	digest := sha256.Sum256([]byte(password))
	hash, err := bcrypt.GenerateFromPassword(digest[:], bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return accessPasswordHashV1 + string(hash), nil
}

// VerifyAccessPassword accepts only the current durable format. Callers that
// support an environment password should check it before this helper.
func VerifyAccessPassword(encoded, password string) bool {
	encoded = strings.TrimSpace(encoded)
	password = strings.TrimSpace(password)
	if !strings.HasPrefix(encoded, accessPasswordHashV1) || password == "" {
		return false
	}
	digest := sha256.Sum256([]byte(password))
	return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(encoded, accessPasswordHashV1)), digest[:]) == nil
}

// migrateLegacySecurityPassword migrates only a password explicitly present in
// the config file. Environment values may seed a missing credential, while a
// persisted hash remains authoritative after a host-local password rotation.
// The returned bool reports that the legacy field must be removed from disk.
func migrateLegacySecurityPassword(cfg *Config, data []byte) (bool, error) {
	if cfg == nil {
		return false, nil
	}
	var raw struct {
		Security map[string]json.RawMessage `json:"security"`
	}
	if json.Unmarshal(data, &raw) != nil || raw.Security == nil {
		return false, nil
	}
	encodedPassword, ok := raw.Security["accessPassword"]
	if !ok {
		return false, nil
	}
	var legacyPassword string
	if err := json.Unmarshal(encodedPassword, &legacyPassword); err != nil {
		return false, err
	}
	legacyPassword = strings.TrimSpace(legacyPassword)
	if strings.TrimSpace(cfg.Security.AccessPasswordHash) == "" && legacyPassword != "" {
		hash, err := HashAccessPassword(legacyPassword)
		if err != nil {
			return false, err
		}
		cfg.Security.AccessPasswordHash = hash
	}
	// Clear the deserialized plaintext even when a hash was already present.
	// applySecurityEnvOverrides may safely restore an environment credential later.
	cfg.Security.AccessPassword = ""
	return true, nil
}

func persistMigratedSecurityPassword(path string, data []byte, passwordHash string) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	security := make(map[string]json.RawMessage)
	if encoded := root["security"]; len(encoded) > 0 && string(encoded) != "null" {
		if err := json.Unmarshal(encoded, &security); err != nil {
			return err
		}
	}
	delete(security, "accessPassword")
	if passwordHash = strings.TrimSpace(passwordHash); passwordHash != "" {
		encoded, err := json.Marshal(passwordHash)
		if err != nil {
			return err
		}
		security["accessPasswordHash"] = encoded
	}
	encodedSecurity, err := json.Marshal(security)
	if err != nil {
		return err
	}
	root["security"] = encodedSecurity
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeSecurityMigrationFile(path, append(updated, '\n'))
}

func writeSecurityMigrationFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".autoto-security-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func normalizeSecurityConfig(security SecurityConfig) SecurityConfig {
	security.AccessPassword = strings.TrimSpace(security.AccessPassword)
	security.AccessPasswordHash = strings.TrimSpace(security.AccessPasswordHash)
	security.DefaultRemoteAccessMode = strings.ToLower(strings.TrimSpace(security.DefaultRemoteAccessMode))
	if security.DefaultRemoteAccessMode != "full" && security.DefaultRemoteAccessMode != "restricted" {
		security.DefaultRemoteAccessMode = "restricted"
	}
	if !security.AllowRemoteFullAccess {
		security.DefaultRemoteAccessMode = "restricted"
	}
	if security.CredentialRevision < 1 {
		security.CredentialRevision = 1
	}
	security.NamedTunnel = normalizeNamedTunnelConfig(security.NamedTunnel)
	return security
}

// normalizeNamedTunnelConfig drops anything it cannot vouch for rather than
// carrying it forward. An unusable hostname or a token reference that is not a
// valid env: reference is discarded, which returns the section to "not
// configured" and leaves the quick tunnel as the only mode.
//
// Failing closed matters more here than preserving user input: a rejected
// plaintext token must not survive in memory or be written back to disk, and a
// tunnel that starts with a malformed hostname would publish traffic to a
// hostname nobody intended.
func normalizeNamedTunnelConfig(tunnel NamedTunnelConfig) NamedTunnelConfig {
	tunnel.Hostname = strings.ToLower(strings.TrimSpace(tunnel.Hostname))
	tunnel.TokenRef = strings.TrimSpace(tunnel.TokenRef)
	if !validTunnelHostname(tunnel.Hostname) {
		tunnel.Hostname = ""
	}
	// ParseRef accepts only env:NAME, so a pasted token is rejected here for the
	// same reason the integration connections reject one.
	if ref, err := secrets.ParseRef(tunnel.TokenRef); err != nil {
		tunnel.TokenRef = ""
	} else {
		tunnel.TokenRef = ref.String()
	}
	if !tunnel.Configured() {
		// AutoStart alone cannot start anything, and leaving it set would silently
		// arm the tunnel the moment the other fields were filled in.
		tunnel.Hostname, tunnel.TokenRef, tunnel.AutoStart = "", "", false
	}
	return tunnel
}

// validTunnelHostname accepts a DNS hostname. It deliberately rejects schemes,
// ports, paths, and userinfo: the value is used to build the public origin shown
// to the user, and accepting "https://host/path" there would produce a URL that
// looks right and is not.
func validTunnelHostname(hostname string) bool {
	if hostname == "" || len(hostname) > 253 || strings.ContainsAny(hostname, ":/@?#\\") {
		return false
	}
	// A named tunnel is always published under a zone, so a bare label such as
	// "localhost" is a configuration mistake rather than a usable target.
	if !strings.Contains(hostname, ".") || strings.HasPrefix(hostname, ".") || strings.HasSuffix(hostname, ".") {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if !validHostnameLabel(label) {
			return false
		}
	}
	return true
}

func validHostnameLabel(label string) bool {
	if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for i := 0; i < len(label); i++ {
		char := label[i]
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return true
}
