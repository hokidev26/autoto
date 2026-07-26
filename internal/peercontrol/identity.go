package peercontrol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const (
	// ProtocolVersion is the current remote peer protocol version.
	ProtocolVersion = 1

	identityFileVersion = 1
	identityAlgorithm   = "Ed25519"
	identityFilename    = "peer-identity.json"
	maxIdentityBytes    = 4096
)

var identityStoreMu sync.Mutex

// PublicIdentity is the non-secret, serializable view of an Identity.
type PublicIdentity struct {
	ProtocolVersion int    `json:"protocolVersion"`
	PublicKey       string `json:"publicKey"`
	Fingerprint     string `json:"fingerprint"`
}

// Verify verifies an Ed25519 signature using this public identity.
func (p PublicIdentity) Verify(message, signature []byte) bool {
	publicKey, err := decodePublicKey(p.PublicKey)
	if err != nil || !validFingerprint(p.Fingerprint) {
		return false
	}
	fingerprint, _ := FingerprintPublicKey(publicKey)
	return constantStringEqual(fingerprint, p.Fingerprint) && ed25519.Verify(publicKey, message, signature)
}

// Identity owns an Ed25519 private key. Its secret key material is deliberately
// kept in unexported fields and is omitted from JSON and formatted output.
type Identity struct {
	public     PublicIdentity
	privateKey ed25519.PrivateKey
}

// Public returns a copy of the identity's public, serializable view.
func (i *Identity) Public() PublicIdentity {
	if i == nil {
		return PublicIdentity{}
	}
	return i.public
}

// Sign signs message with the identity's Ed25519 private key.
func (i *Identity) Sign(message []byte) ([]byte, error) {
	if i == nil || len(i.privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("peer identity is unavailable")
	}
	return ed25519.Sign(i.privateKey, message), nil
}

// Verify verifies a signature using the identity's public key.
func (i *Identity) Verify(message, signature []byte) bool {
	return i != nil && i.public.Verify(message, signature)
}

// String intentionally exposes only the public identity.
func (i *Identity) String() string {
	if i == nil {
		return "peercontrol.Identity<nil>"
	}
	return fmt.Sprintf("peercontrol.Identity{ProtocolVersion:%d PublicKey:%q Fingerprint:%q}", i.public.ProtocolVersion, i.public.PublicKey, i.public.Fingerprint)
}

// GoString intentionally exposes only the public identity.
func (i *Identity) GoString() string { return i.String() }

// IdentityStore persists the host peer identity below a fixed home directory.
type IdentityStore struct {
	homeDir    string
	secretsDir string
	path       string
}

// NewIdentityStore validates homeDir and returns a store rooted at
// <homeDir>/secrets/peer-identity.json.
func NewIdentityStore(homeDir string) (*IdentityStore, error) {
	if strings.TrimSpace(homeDir) == "" || strings.IndexByte(homeDir, 0) >= 0 || containsParentTraversal(homeDir) {
		return nil, errors.New("peer identity home directory is invalid")
	}
	absolute, err := filepath.Abs(homeDir)
	if err != nil {
		return nil, errors.New("resolve peer identity home directory")
	}
	absolute = filepath.Clean(absolute)
	secretsDir := filepath.Join(absolute, "secrets")
	path := filepath.Join(secretsDir, identityFilename)
	relative, err := filepath.Rel(absolute, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return nil, errors.New("peer identity path escapes home directory")
	}
	return &IdentityStore{homeDir: absolute, secretsDir: secretsDir, path: path}, nil
}

// Path returns the fixed identity file path. It contains no key material.
func (s *IdentityStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// LoadOrCreate loads the durable identity or atomically installs a newly
// generated one. Concurrent callers and processes converge on the same file.
func (s *IdentityStore) LoadOrCreate() (*Identity, error) {
	if s == nil {
		return nil, errors.New("peer identity store is nil")
	}
	identityStoreMu.Lock()
	defer identityStoreMu.Unlock()

	if err := s.prepareDirectories(); err != nil {
		return nil, err
	}
	identity, err := s.load()
	if err == nil {
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := s.create(); err != nil {
		return nil, err
	}
	return s.load()
}

type identityFile struct {
	Version   int    `json:"version"`
	Algorithm string `json:"algorithm"`
	Seed      string `json:"seed"`
}

func (s *IdentityStore) prepareDirectories() error {
	if err := rejectSymlinkPathComponents(s.homeDir); err != nil {
		return err
	}
	homeInfo, homeErr := os.Lstat(s.homeDir)
	homeCreated := errors.Is(homeErr, os.ErrNotExist)
	if homeErr != nil && !homeCreated {
		return errors.New("inspect peer identity home directory")
	}
	if homeCreated {
		if err := os.MkdirAll(s.homeDir, 0o700); err != nil {
			return errors.New("create peer identity home directory")
		}
	}
	if err := rejectSymlinkPathComponents(s.homeDir); err != nil {
		return err
	}
	homeInfo, err := os.Lstat(s.homeDir)
	if err != nil || !homeInfo.IsDir() || homeInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("peer identity home path is not a real directory")
	}
	if homeCreated && runtime.GOOS != "windows" {
		if err := os.Chmod(s.homeDir, 0o700); err != nil {
			return errors.New("secure peer identity home directory")
		}
	}

	if info, err := os.Lstat(s.secretsDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("peer identity secrets path is not a real directory")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(s.secretsDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return errors.New("create peer identity secrets directory")
		}
	} else {
		return errors.New("inspect peer identity secrets directory")
	}
	if err := rejectSymlinkPathComponents(s.secretsDir); err != nil {
		return err
	}
	info, err := os.Lstat(s.secretsDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("peer identity secrets path is not a real directory")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(s.secretsDir, 0o700); err != nil {
			return errors.New("secure peer identity secrets directory")
		}
	}
	return nil
}

func (s *IdentityStore) load() (*Identity, error) {
	before, err := os.Lstat(s.path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, errors.New("peer identity file is not a regular file")
	}
	if before.Size() <= 0 || before.Size() > maxIdentityBytes {
		return nil, errors.New("peer identity file has an invalid size")
	}
	file, err := os.Open(s.path)
	if err != nil {
		return nil, errors.New("open peer identity file")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, errors.New("peer identity file changed during open")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxIdentityBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxIdentityBytes {
		return nil, errors.New("read peer identity file")
	}
	after, err := os.Lstat(s.path)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) {
		return nil, errors.New("peer identity file changed during read")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0o600 {
		return nil, errors.New("peer identity file permissions are not 0600")
	}
	return decodeIdentityFile(data)
}

func (s *IdentityStore) create() error {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return errors.New("generate peer identity")
	}
	seed := privateKey.Seed()
	defer clear(seed)
	encoded, err := json.Marshal(identityFile{
		Version:   identityFileVersion,
		Algorithm: identityAlgorithm,
		Seed:      base64.StdEncoding.EncodeToString(seed),
	})
	if err != nil {
		return errors.New("encode peer identity")
	}
	encoded = append(encoded, '\n')

	temporary, err := os.CreateTemp(s.secretsDir, ".peer-identity-*")
	if err != nil {
		return errors.New("create peer identity temporary file")
	}
	temporaryPath := temporary.Name()
	complete := false
	defer func() {
		_ = temporary.Close()
		if !complete {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return errors.New("secure peer identity temporary file")
	}
	if _, err := temporary.Write(encoded); err != nil {
		return errors.New("write peer identity temporary file")
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("sync peer identity temporary file")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close peer identity temporary file")
	}
	installed, err := installIdentityFile(temporaryPath, s.path)
	if err != nil {
		return errors.New("atomically install peer identity file")
	}
	if installed {
		complete = true
		if runtime.GOOS != "windows" {
			if err := os.Chmod(s.path, 0o600); err != nil {
				return errors.New("secure peer identity file")
			}
		}
		return syncDirectory(s.secretsDir)
	}
	return nil
}

func decodeIdentityFile(data []byte) (*Identity, error) {
	if len(data) == 0 || len(data) > maxIdentityBytes {
		return nil, errors.New("invalid peer identity file")
	}
	var persisted identityFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&persisted); err != nil {
		return nil, errors.New("invalid peer identity file")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("invalid peer identity file")
	}
	if persisted.Version != identityFileVersion || persisted.Algorithm != identityAlgorithm {
		return nil, errors.New("unsupported peer identity file")
	}
	seed, err := base64.StdEncoding.Strict().DecodeString(persisted.Seed)
	if err != nil || len(seed) != ed25519.SeedSize || base64.StdEncoding.EncodeToString(seed) != persisted.Seed {
		return nil, errors.New("invalid peer identity seed")
	}
	defer clear(seed)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	fingerprint, _ := FingerprintPublicKey(publicKey)
	return &Identity{
		public: PublicIdentity{
			ProtocolVersion: ProtocolVersion,
			PublicKey:       base64.StdEncoding.EncodeToString(publicKey),
			Fingerprint:     fingerprint,
		},
		privateKey: privateKey,
	}, nil
}

// FingerprintPublicKey returns the stable lowercase SHA-256 fingerprint of a
// raw Ed25519 public key.
func FingerprintPublicKey(publicKey []byte) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", errors.New("invalid Ed25519 public key length")
	}
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:]), nil
}

func decodePublicKey(encoded string) (ed25519.PublicKey, error) {
	if len(encoded) > 128 || strings.TrimSpace(encoded) != encoded {
		return nil, errors.New("invalid Ed25519 public key")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}

func validFingerprint(fingerprint string) bool {
	if len(fingerprint) != sha256.Size*2 || strings.ToLower(fingerprint) != fingerprint {
		return false
	}
	decoded, err := hex.DecodeString(fingerprint)
	return err == nil && len(decoded) == sha256.Size
}

func constantStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range len(left) {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func containsParentTraversal(path string) bool {
	for _, component := range strings.FieldsFunc(path, func(character rune) bool {
		return character == '/' || character == '\\'
	}) {
		if component == ".." {
			return true
		}
	}
	return false
}

func rejectSymlinkPathComponents(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errors.New("peer identity path is invalid")
	}
	absolute = filepath.Clean(absolute)
	volume := filepath.VolumeName(absolute)
	remainder := strings.TrimPrefix(absolute, volume)
	current := volume + string(filepath.Separator)
	for _, component := range strings.Split(strings.Trim(remainder, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("inspect peer identity path")
		}
		if info.Mode()&os.ModeSymlink != 0 || (current != absolute && !info.IsDir()) {
			return errors.New("peer identity path contains a symlink or non-directory component")
		}
	}
	return nil
}

func syncDirectory(directory string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	file, err := os.Open(directory)
	if err != nil {
		return errors.New("open peer identity directory for sync")
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return errors.New("sync peer identity directory")
	}
	return nil
}
