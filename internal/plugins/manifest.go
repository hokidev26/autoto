// Package plugins validates local plugin manifests without resolving secret values.
package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"autoto/internal/secrets"
)

const (
	ManifestFilename          = "autoto.plugin.json"
	APIVersionV1Alpha1        = "autoto.dev/v1alpha1"
	TransportStdio            = "stdio"
	MaxManifestBytes          = 64 << 10
	MaxManifestArgs           = 64
	MaxManifestArgBytes       = 4096
	MaxManifestTimeoutSeconds = 600
)

// Manifest is the normalized, service-ready representation of autoto.plugin.json.
// SecretRefs contains only logical env:VARIABLE_NAME references, never resolved values.
// TimeoutSeconds of 0 means the manifest did not set a timeout and the service
// default applies.
type Manifest struct {
	APIVersion     string            `json:"apiVersion"`
	Transport      string            `json:"transport"`
	Slug           string            `json:"slug"`
	Name           string            `json:"name"`
	Version        string            `json:"version"`
	Description    string            `json:"description,omitempty"`
	RootPath       string            `json:"rootPath"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	SecretRefs     map[string]string `json:"secretRefs"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
	Hash           string            `json:"manifestHash"`
}

// manifestFile is also the canonical hashing form. timeoutSeconds must keep
// omitempty so manifests without the field retain their pre-existing hash.
type manifestFile struct {
	APIVersion     string            `json:"apiVersion"`
	Transport      string            `json:"transport"`
	Slug           string            `json:"slug"`
	Name           string            `json:"name"`
	Version        string            `json:"version"`
	Description    string            `json:"description"`
	Command        string            `json:"command"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	SecretRefs     map[string]string `json:"secretRefs"`
	TimeoutSeconds int               `json:"timeoutSeconds,omitempty"`
}

// LoadManifest reads and validates autoto.plugin.json from rootPath.
func LoadManifest(rootPath string) (Manifest, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return Manifest{}, errors.New("plugin root path is required")
	}
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return Manifest{}, errors.New("cannot resolve plugin root path")
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve plugin root path: %w", err)
	}
	info, err := os.Stat(realRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("stat plugin root path: %w", err)
	}
	if !info.IsDir() {
		return Manifest{}, errors.New("plugin root path must be a directory")
	}

	manifestPath := filepath.Join(realRoot, ManifestFilename)
	realManifestPath, err := filepath.EvalSymlinks(manifestPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve plugin manifest: %w", err)
	}
	if !pathWithin(realRoot, realManifestPath) {
		return Manifest{}, errors.New("plugin manifest escapes plugin root")
	}
	file, err := os.Open(realManifestPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("open plugin manifest: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read plugin manifest: %w", err)
	}
	if len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("plugin manifest exceeds %d bytes", MaxManifestBytes)
	}
	return ParseManifest(realRoot, data)
}

// ReadManifest is an alias for LoadManifest.
func ReadManifest(rootPath string) (Manifest, error) { return LoadManifest(rootPath) }

// ParseManifest validates manifest bytes relative to an existing plugin root.
func ParseManifest(rootPath string, data []byte) (Manifest, error) {
	if len(data) == 0 {
		return Manifest{}, errors.New("plugin manifest is empty")
	}
	if len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("plugin manifest exceeds %d bytes", MaxManifestBytes)
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		return Manifest{}, errors.New("plugin manifest must be valid UTF-8 without NUL bytes")
	}
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return Manifest{}, errors.New("cannot resolve plugin root path")
	}
	realRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve plugin root path: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var raw manifestFile
	if err := decoder.Decode(&raw); err != nil {
		return Manifest{}, fmt.Errorf("decode plugin manifest: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Manifest{}, err
	}
	manifest, err := normalizeManifest(realRoot, raw)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Hash, err = hashManifest(manifest)
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// NormalizeSlug converts a user-facing slug to lowercase ASCII kebab-case.
func NormalizeSlug(value string) (string, error) {
	var builder strings.Builder
	lastDash := false
	for _, char := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
			builder.WriteRune(char)
			lastDash = false
		default:
			if builder.Len() > 0 && !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" || len(slug) > 63 {
		return "", errors.New("plugin slug must normalize to 1-63 lowercase ASCII characters")
	}
	return slug, nil
}

func normalizeManifest(root string, raw manifestFile) (Manifest, error) {
	if strings.TrimSpace(raw.APIVersion) != APIVersionV1Alpha1 {
		return Manifest{}, fmt.Errorf("plugin apiVersion must be %s", APIVersionV1Alpha1)
	}
	if strings.TrimSpace(raw.Transport) != TransportStdio {
		return Manifest{}, errors.New("plugin transport must be stdio")
	}
	slug, err := NormalizeSlug(raw.Slug)
	if err != nil {
		return Manifest{}, err
	}
	name := strings.TrimSpace(raw.Name)
	version := strings.TrimSpace(raw.Version)
	description := strings.TrimSpace(raw.Description)
	if err := validText("name", name, 120, true); err != nil {
		return Manifest{}, err
	}
	if err := validText("version", version, 64, true); err != nil {
		return Manifest{}, err
	}
	if err := validText("description", description, 1000, false); err != nil {
		return Manifest{}, err
	}
	command, err := normalizeCommand(root, raw.Command)
	if err != nil {
		return Manifest{}, err
	}
	args, err := normalizeArgs(raw.Args)
	if err != nil {
		return Manifest{}, err
	}
	env, refs, err := normalizeEnvironment(raw.Env, raw.SecretRefs)
	if err != nil {
		return Manifest{}, err
	}
	if raw.TimeoutSeconds < 0 || raw.TimeoutSeconds > MaxManifestTimeoutSeconds {
		return Manifest{}, fmt.Errorf("plugin timeoutSeconds must be between 1 and %d, or omitted", MaxManifestTimeoutSeconds)
	}
	return Manifest{APIVersion: APIVersionV1Alpha1, Transport: TransportStdio, Slug: slug, Name: name, Version: version, Description: description, RootPath: root, Command: command, Args: args, Env: env, SecretRefs: refs, TimeoutSeconds: raw.TimeoutSeconds}, nil
}

func normalizeCommand(root, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("plugin command is required")
	}
	if len(value) > 1024 || strings.ContainsRune(value, 0) || !utf8.ValidString(value) || filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return "", errors.New("plugin command must be a relative path inside the plugin root")
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("plugin command must not contain parent traversal")
	}
	for _, component := range strings.Split(filepath.ToSlash(value), "/") {
		if component == ".." {
			return "", errors.New("plugin command must not contain parent traversal")
		}
	}
	candidate := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve plugin command: %w", err)
	}
	if !pathWithin(root, resolved) {
		return "", errors.New("plugin command escapes plugin root through a symlink")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat plugin command: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("plugin command must be a regular file")
	}
	return filepath.ToSlash(clean), nil
}

func normalizeArgs(input []string) ([]string, error) {
	if len(input) > MaxManifestArgs {
		return nil, fmt.Errorf("plugin args exceed maximum count %d", MaxManifestArgs)
	}
	args := make([]string, len(input))
	total := 0
	for index, value := range input {
		if len(value) > MaxManifestArgBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("plugin arg %d is invalid or exceeds %d bytes", index, MaxManifestArgBytes)
		}
		total += len(value)
		if total > 32<<10 {
			return nil, errors.New("plugin args exceed total size limit")
		}
		args[index] = value
	}
	return args, nil
}

func normalizeEnvironment(env, refs map[string]string) (map[string]string, map[string]string, error) {
	if len(env)+len(refs) > 128 {
		return nil, nil, errors.New("plugin environment exceeds maximum key count 128")
	}
	normalizedEnv := make(map[string]string, len(env))
	for rawKey, value := range env {
		key := strings.TrimSpace(rawKey)
		if !validEnvName(key) || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return nil, nil, fmt.Errorf("invalid plugin env entry %q", rawKey)
		}
		if sensitiveKey(key) {
			return nil, nil, fmt.Errorf("sensitive plugin env key %q must use secretRefs with an env:VARIABLE_NAME reference", key)
		}
		normalizedEnv[key] = value
	}
	normalizedRefs := make(map[string]string, len(refs))
	for rawKey, value := range refs {
		key := strings.TrimSpace(rawKey)
		if !validEnvName(key) {
			return nil, nil, fmt.Errorf("invalid plugin secretRefs key %q", rawKey)
		}
		if _, duplicate := normalizedEnv[key]; duplicate {
			return nil, nil, fmt.Errorf("plugin environment key %q appears in both env and secretRefs", key)
		}
		ref, err := secrets.ParseRef(value)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid plugin secret reference for %q: %w", key, err)
		}
		normalizedRefs[key] = ref.String()
	}
	return normalizedEnv, normalizedRefs, nil
}

func hashManifest(manifest Manifest) (string, error) {
	canonical := manifestFile{APIVersion: manifest.APIVersion, Transport: manifest.Transport, Slug: manifest.Slug, Name: manifest.Name, Version: manifest.Version, Description: manifest.Description, Command: manifest.Command, Args: manifest.Args, Env: manifest.Env, SecretRefs: manifest.SecretRefs, TimeoutSeconds: manifest.TimeoutSeconds}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode normalized plugin manifest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode plugin manifest: %w", err)
	}
	return errors.New("plugin manifest must contain exactly one JSON object")
}

func validText(name, value string, maxBytes int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("plugin %s is required", name)
	}
	if len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("invalid plugin %s", name)
	}
	return nil
}

func validEnvName(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(key))
	for _, marker := range []string{"password", "passwd", "secret", "token", "apikey", "credential", "privatekey", "accesskey", "authorization", "cookie", "bearer", "jwt"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
