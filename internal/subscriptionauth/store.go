// Package subscriptionauth persists OAuth credentials shared by subscription providers.
package subscriptionauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"autoto/internal/secrets"
)

const (
	ProviderGemini = "gemini"
	ProviderGrok   = "grok"
	ProviderKimi   = "kimi"
	ProviderKiro   = "kiro"

	DefaultPriority = 100

	credentialDirMode    = 0o700
	credentialFileMode   = 0o600
	credentialFileSuffix = ".json"
	maxCredentialBytes   = 128 << 10
	maxAliasBytes        = 200
	maxTokenBytes        = 16 << 10
	maxIdentityBytes     = 1024
	maxScopeBytes        = 4096
	maxEndpointBytes     = 2048
	maxPriority          = 1_000_000
	maxFilenameBytes     = 160
)

// Credential is the private on-disk representation. Token fields must never be
// included in summaries, logs, or error messages.
type Credential struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Alias         string `json:"alias,omitempty"`
	Priority      int    `json:"priority"`
	Disabled      bool   `json:"disabled,omitempty"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	IDToken       string `json:"id_token,omitempty"`
	TokenType     string `json:"token_type,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Email         string `json:"email,omitempty"`
	Subject       string `json:"subject,omitempty"`
	Scope         string `json:"scope,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// StoredCredential pairs a credential with its safe, store-relative filename.
type StoredCredential struct {
	Filename string `json:"-"`
	Credential
}

// CreateRequest contains OAuth material and user-managed metadata. Store
// generates the ID and timestamps.
type CreateRequest struct {
	Provider      string `json:"provider"`
	Alias         string `json:"alias,omitempty"`
	Priority      int    `json:"priority,omitempty"`
	Disabled      bool   `json:"disabled,omitempty"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	IDToken       string `json:"id_token,omitempty"`
	TokenType     string `json:"token_type,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Email         string `json:"email,omitempty"`
	Subject       string `json:"subject,omitempty"`
	Scope         string `json:"scope,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
}

// MetadataUpdate changes only user-managed selection metadata.
type MetadataUpdate struct {
	Alias    *string `json:"alias,omitempty"`
	Priority *int    `json:"priority,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

// TokenUpdate replaces OAuth material after login or refresh. An empty
// RefreshToken deliberately preserves the currently stored refresh token.
type TokenUpdate struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	IDToken       string `json:"id_token,omitempty"`
	TokenType     string `json:"token_type,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Email         string `json:"email,omitempty"`
	Subject       string `json:"subject,omitempty"`
	Scope         string `json:"scope,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
}

// AccountSummary is safe for API responses and intentionally has no token fields.
type AccountSummary struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Alias         string `json:"alias,omitempty"`
	Priority      int    `json:"priority"`
	Disabled      bool   `json:"disabled"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	Email         string `json:"email,omitempty"`
	Subject       string `json:"subject,omitempty"`
	Scope         string `json:"scope,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	TokenEndpoint string `json:"token_endpoint,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// Summary returns a token-free account representation.
func Summary(item StoredCredential) AccountSummary {
	credential := item.Credential
	alias := credential.Alias
	for _, secret := range []string{credential.AccessToken, credential.RefreshToken, credential.IDToken} {
		if secret != "" && strings.Contains(alias, secret) {
			alias = ""
			break
		}
	}
	return AccountSummary{
		ID:            credential.ID,
		Provider:      credential.Provider,
		Alias:         alias,
		Priority:      credential.Priority,
		Disabled:      credential.Disabled,
		ExpiresAt:     credential.ExpiresAt,
		Email:         credential.Email,
		Subject:       credential.Subject,
		Scope:         credential.Scope,
		ProjectID:     credential.ProjectID,
		DeviceID:      credential.DeviceID,
		TokenEndpoint: credential.TokenEndpoint,
		CreatedAt:     credential.CreatedAt,
		UpdatedAt:     credential.UpdatedAt,
	}
}

type Store struct {
	dir        string
	unsafePath bool
	lock       *sync.RWMutex
	cipher     *secrets.CredentialCipher
}

var storeLocks sync.Map

// DefaultStoreDir returns the provider-specific location below Autoto's home.
func DefaultStoreDir(home, provider string) string {
	home = strings.TrimSpace(home)
	provider = normalizeProvider(provider)
	if home == "" || !validProvider(provider) {
		return ""
	}
	return filepath.Join(home, "credentials", provider)
}

func NewStore(dir string) *Store {
	dir = strings.TrimSpace(dir)
	unsafePath := false
	if dir != "" {
		if absolute, err := filepath.Abs(dir); err == nil {
			dir = filepath.Clean(absolute)
		} else {
			unsafePath = true
		}
		if !unsafePath {
			if err := rejectSymlinkPathComponents(dir); err != nil {
				unsafePath = true
			}
		}
	}
	key := filepath.Clean(dir)
	if dir == "" {
		key = ""
	}
	lockValue, _ := storeLocks.LoadOrStore(key, &sync.RWMutex{})
	var cipher *secrets.CredentialCipher
	if dir != "" {
		cipher = secrets.NewCredentialCipher("subscription", credentialKeyPath(dir))
	}
	return &Store{dir: dir, unsafePath: unsafePath, lock: lockValue.(*sync.RWMutex), cipher: cipher}
}

// credentialKeyPath places the encryption key next to (not inside) the
// credential directory so copying the directory alone cannot recover tokens.
func credentialKeyPath(dir string) string {
	return filepath.Clean(dir) + ".key"
}

func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Load returns all unique accounts, sorted by priority and then opaque ID.
func (s *Store) Load() ([]StoredCredential, error) {
	if err := s.validateStore(); err != nil {
		return nil, err
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.loadLocked()
}

// List returns enabled credentials only.
func (s *Store) List() ([]StoredCredential, error) {
	items, err := s.Load()
	if err != nil {
		return nil, err
	}
	result := make([]StoredCredential, 0, len(items))
	for _, item := range items {
		if !item.Disabled {
			result = append(result, item)
		}
	}
	return result, nil
}

func (s *Store) ListAccounts() ([]AccountSummary, error) {
	items, err := s.Load()
	if err != nil {
		return nil, err
	}
	result := make([]AccountSummary, 0, len(items))
	for _, item := range items {
		result = append(result, Summary(item))
	}
	return result, nil
}

func (s *Store) Configured() bool {
	items, err := s.List()
	return err == nil && len(items) > 0
}

// CreateOAuth saves a new OAuth account, or returns the existing account when
// the same provider identity is already present.
func (s *Store) CreateOAuth(request CreateRequest) (StoredCredential, error) {
	if err := s.validateStore(); err != nil {
		return StoredCredential{}, err
	}
	credential := normalizeCredential(Credential{
		Provider:      request.Provider,
		Alias:         request.Alias,
		Priority:      request.Priority,
		Disabled:      request.Disabled,
		AccessToken:   request.AccessToken,
		RefreshToken:  request.RefreshToken,
		IDToken:       request.IDToken,
		TokenType:     request.TokenType,
		ExpiresAt:     request.ExpiresAt,
		Email:         request.Email,
		Subject:       request.Subject,
		Scope:         request.Scope,
		ProjectID:     request.ProjectID,
		DeviceID:      request.DeviceID,
		TokenEndpoint: request.TokenEndpoint,
	})
	if credential.Priority == 0 {
		credential.Priority = DefaultPriority
	}
	if err := validateCredential(credential, false); err != nil {
		return StoredCredential{}, err
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.ensureDirLocked(); err != nil {
		return StoredCredential{}, err
	}
	existing, err := s.loadLocked()
	if err != nil {
		return StoredCredential{}, err
	}
	identity := credentialIdentity(credential)
	for _, item := range existing {
		if credentialIdentity(item.Credential) == identity {
			return item, nil
		}
	}
	credential.ID, err = newCredentialID()
	if err != nil {
		return StoredCredential{}, errors.New("无法分配订阅凭据 ID")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	credential.CreatedAt = now
	credential.UpdatedAt = now
	filename := credential.ID + credentialFileSuffix
	if err := s.writeCredentialLocked(filename, credential); err != nil {
		return StoredCredential{}, err
	}
	return StoredCredential{Filename: filename, Credential: credential}, nil
}

func (s *Store) GetByID(id string) (StoredCredential, error) {
	id = strings.TrimSpace(id)
	if !validCredentialID(id) {
		return StoredCredential{}, os.ErrNotExist
	}
	items, err := s.Load()
	if err != nil {
		return StoredCredential{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return StoredCredential{}, os.ErrNotExist
}

func (s *Store) UpdateMetadata(id string, update MetadataUpdate) (StoredCredential, error) {
	if err := s.validateStore(); err != nil {
		return StoredCredential{}, err
	}
	id = strings.TrimSpace(id)
	if !validCredentialID(id) {
		return StoredCredential{}, os.ErrNotExist
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	items, err := s.loadLocked()
	if err != nil {
		return StoredCredential{}, err
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		if update.Alias != nil {
			item.Alias = strings.TrimSpace(*update.Alias)
		}
		if update.Priority != nil {
			item.Priority = *update.Priority
		}
		if update.Disabled != nil {
			item.Disabled = *update.Disabled
		}
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := validateCredential(item.Credential, true); err != nil {
			return StoredCredential{}, err
		}
		if err := s.writeCredentialLocked(item.Filename, item.Credential); err != nil {
			return StoredCredential{}, err
		}
		return item, nil
	}
	return StoredCredential{}, os.ErrNotExist
}

func (s *Store) UpdateTokens(id string, update TokenUpdate) (StoredCredential, error) {
	if err := s.validateStore(); err != nil {
		return StoredCredential{}, err
	}
	id = strings.TrimSpace(id)
	if !validCredentialID(id) {
		return StoredCredential{}, os.ErrNotExist
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	items, err := s.loadLocked()
	if err != nil {
		return StoredCredential{}, err
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		item.AccessToken = strings.TrimSpace(update.AccessToken)
		if refresh := strings.TrimSpace(update.RefreshToken); refresh != "" {
			item.RefreshToken = refresh
		}
		item.IDToken = strings.TrimSpace(update.IDToken)
		item.TokenType = strings.TrimSpace(update.TokenType)
		item.ExpiresAt = strings.TrimSpace(update.ExpiresAt)
		item.Email = strings.TrimSpace(update.Email)
		item.Subject = strings.TrimSpace(update.Subject)
		item.Scope = strings.TrimSpace(update.Scope)
		item.ProjectID = strings.TrimSpace(update.ProjectID)
		item.DeviceID = strings.TrimSpace(update.DeviceID)
		item.TokenEndpoint = strings.TrimSpace(update.TokenEndpoint)
		item.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := validateCredential(item.Credential, true); err != nil {
			return StoredCredential{}, err
		}
		if err := s.writeCredentialLocked(item.Filename, item.Credential); err != nil {
			return StoredCredential{}, err
		}
		return item, nil
	}
	return StoredCredential{}, os.ErrNotExist
}

func (s *Store) Delete(id string) error {
	if err := s.validateStore(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if !validCredentialID(id) {
		return os.ErrNotExist
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	items, err := s.loadLocked()
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID != id {
			continue
		}
		filename, err := safeCredentialFilename(item.Filename)
		if err != nil {
			return err
		}
		path := filepath.Join(s.dir, filename)
		info, err := os.Lstat(path)
		if err != nil {
			return errors.New("无法检查订阅凭据")
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("订阅凭据目标路径不安全")
		}
		if err := os.Remove(path); err != nil {
			return errors.New("删除订阅凭据失败")
		}
		return syncDirectory(s.dir)
	}
	return os.ErrNotExist
}

func (s *Store) validateStore() error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return errors.New("订阅凭据库路径未配置")
	}
	if s.unsafePath {
		return errors.New("订阅凭据库路径不安全")
	}
	return nil
}

func (s *Store) loadLocked() ([]StoredCredential, error) {
	if err := rejectSymlinkPathComponents(s.dir); err != nil {
		return nil, err
	}
	info, statErr := os.Lstat(s.dir)
	if errors.Is(statErr, os.ErrNotExist) {
		return []StoredCredential{}, nil
	}
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("订阅凭据库路径不安全")
	}
	if err := os.Chmod(s.dir, credentialDirMode); err != nil {
		return nil, errors.New("设置订阅凭据库权限失败")
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, errors.New("读取订阅凭据库失败")
	}
	items := make([]StoredCredential, 0, len(entries))
	usedIDs := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), credentialFileSuffix) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		filename, err := safeCredentialFilename(entry.Name())
		if err != nil {
			continue
		}
		path := filepath.Join(s.dir, filename)
		fileInfo, err := os.Lstat(path)
		if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, errors.New("读取订阅凭据失败")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(data) > maxCredentialBytes {
			return nil, errors.New("读取订阅凭据失败")
		}
		migrated := false
		if secrets.IsEncryptedCredential(data) {
			plaintext, err := s.cipher.Decrypt(data)
			if err != nil {
				// The key file is gone or no longer matches (e.g. it was deleted
				// and re-login created a fresh key), so this credential can never
				// be decrypted again. Treat it as absent so the store reports
				// "not configured" and the user can sign in again instead of
				// every load failing.
				continue
			}
			data = plaintext
		} else {
			// Legacy plaintext credential file: re-persist encrypted below.
			migrated = true
		}
		credential, err := decodeCredential(data)
		if err != nil || !validCredentialID(credential.ID) {
			return nil, errors.New("订阅凭据已损坏")
		}
		if _, duplicate := usedIDs[credential.ID]; duplicate {
			return nil, errors.New("订阅凭据包含重复 ID")
		}
		usedIDs[credential.ID] = struct{}{}
		if migrated {
			if err := s.writeCredentialLocked(filename, credential); err != nil {
				return nil, errors.New("迁移订阅凭据失败")
			}
		} else if err := os.Chmod(path, credentialFileMode); err != nil {
			return nil, errors.New("设置订阅凭据权限失败")
		}
		items = append(items, StoredCredential{Filename: filename, Credential: credential})
	}
	sortCredentials(items)
	unique := items[:0]
	identities := make(map[string]struct{}, len(items))
	for _, item := range items {
		identity := credentialIdentity(item.Credential)
		if _, duplicate := identities[identity]; duplicate {
			continue
		}
		identities[identity] = struct{}{}
		unique = append(unique, item)
	}
	return unique, nil
}

func (s *Store) ensureDirLocked() error {
	if err := rejectSymlinkPathComponents(s.dir); err != nil {
		return err
	}
	parent := filepath.Dir(s.dir)
	if parent != "." && parent != s.dir {
		if err := os.MkdirAll(parent, credentialDirMode); err != nil {
			return errors.New("创建订阅凭据库失败")
		}
		if err := rejectSymlinkPathComponents(parent); err != nil {
			return err
		}
	}
	if info, err := os.Lstat(s.dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("订阅凭据库路径不安全")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("检查订阅凭据库失败")
	} else if err := os.Mkdir(s.dir, credentialDirMode); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("创建订阅凭据库失败")
	}
	if err := os.Chmod(s.dir, credentialDirMode); err != nil {
		return errors.New("设置订阅凭据库权限失败")
	}
	return nil
}

func (s *Store) writeCredentialLocked(filename string, credential Credential) error {
	filename, err := safeCredentialFilename(filename)
	if err != nil {
		return err
	}
	credential = normalizeCredential(credential)
	if err := validateCredential(credential, true); err != nil {
		return err
	}
	data, err := json.MarshalIndent(credential, "", "  ")
	if err != nil || len(data)+1 > maxCredentialBytes {
		return errors.New("序列化订阅凭据失败")
	}
	data = append(data, '\n')
	data, err = s.cipher.Encrypt(data)
	if err != nil || len(data) > maxCredentialBytes {
		return errors.New("加密订阅凭据失败")
	}
	temp, err := os.CreateTemp(s.dir, ".subscription-credential-*")
	if err != nil {
		return errors.New("创建订阅凭据临时文件失败")
	}
	tempName := temp.Name()
	complete := false
	defer func() {
		_ = temp.Close()
		if !complete {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(credentialFileMode); err != nil {
		return errors.New("设置订阅凭据权限失败")
	}
	if _, err := temp.Write(data); err != nil {
		return errors.New("写入订阅凭据失败")
	}
	if err := temp.Sync(); err != nil {
		return errors.New("同步订阅凭据失败")
	}
	if err := temp.Close(); err != nil {
		return errors.New("关闭订阅凭据失败")
	}
	target := filepath.Join(s.dir, filename)
	if info, err := os.Lstat(target); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("订阅凭据目标路径不安全")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("检查订阅凭据目标失败")
	}
	if err := os.Rename(tempName, target); err != nil {
		if runtime.GOOS == "windows" {
			if removeErr := os.Remove(target); removeErr == nil {
				err = os.Rename(tempName, target)
			}
		}
		if err != nil {
			return errors.New("保存订阅凭据失败")
		}
	}
	if err := os.Chmod(target, credentialFileMode); err != nil {
		return errors.New("设置订阅凭据权限失败")
	}
	complete = true
	return syncDirectory(s.dir)
}

func decodeCredential(data []byte) (Credential, error) {
	if len(data) == 0 || len(data) > maxCredentialBytes {
		return Credential{}, errors.New("invalid credential")
	}
	var credential Credential
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil {
		return Credential{}, errors.New("invalid credential")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Credential{}, errors.New("invalid credential")
	}
	credential = normalizeCredential(credential)
	if err := validateCredential(credential, true); err != nil {
		return Credential{}, errors.New("invalid credential")
	}
	return credential, nil
}

func normalizeCredential(credential Credential) Credential {
	credential.ID = strings.TrimSpace(credential.ID)
	credential.Provider = normalizeProvider(credential.Provider)
	credential.Alias = strings.TrimSpace(credential.Alias)
	credential.AccessToken = strings.TrimSpace(credential.AccessToken)
	credential.RefreshToken = strings.TrimSpace(credential.RefreshToken)
	credential.IDToken = strings.TrimSpace(credential.IDToken)
	credential.TokenType = strings.TrimSpace(credential.TokenType)
	credential.ExpiresAt = strings.TrimSpace(credential.ExpiresAt)
	credential.Email = strings.TrimSpace(credential.Email)
	credential.Subject = strings.TrimSpace(credential.Subject)
	credential.Scope = strings.TrimSpace(credential.Scope)
	credential.ProjectID = strings.TrimSpace(credential.ProjectID)
	credential.DeviceID = strings.TrimSpace(credential.DeviceID)
	credential.TokenEndpoint = strings.TrimSpace(credential.TokenEndpoint)
	credential.CreatedAt = strings.TrimSpace(credential.CreatedAt)
	credential.UpdatedAt = strings.TrimSpace(credential.UpdatedAt)
	return credential
}

func validateCredential(credential Credential, persisted bool) error {
	if !validProvider(credential.Provider) {
		return errors.New("订阅凭据 provider 无效")
	}
	if !validText(credential.Alias, maxAliasBytes) {
		return fmt.Errorf("订阅账号别名不能超过 %d 字节", maxAliasBytes)
	}
	if credential.Priority < 0 || credential.Priority > maxPriority || persisted && credential.Priority == 0 {
		return fmt.Errorf("订阅账号优先级必须在 1 到 %d 之间", maxPriority)
	}
	for _, token := range []string{credential.AccessToken, credential.RefreshToken, credential.IDToken} {
		if !validSecret(token, maxTokenBytes) {
			return fmt.Errorf("订阅 OAuth token 不能超过 %d 字节", maxTokenBytes)
		}
	}
	if credential.AccessToken == "" {
		return errors.New("订阅 OAuth 凭据缺少 access token")
	}
	for _, field := range []string{credential.TokenType, credential.Email, credential.Subject, credential.ProjectID, credential.DeviceID} {
		if !validText(field, maxIdentityBytes) {
			return errors.New("订阅 OAuth 凭据身份字段无效")
		}
	}
	if !validText(credential.Scope, maxScopeBytes) {
		return errors.New("订阅 OAuth scope 无效")
	}
	if !validText(credential.TokenEndpoint, maxEndpointBytes) || credential.TokenEndpoint != "" && !validTokenEndpoint(credential.TokenEndpoint) {
		return errors.New("订阅 OAuth token endpoint 无效")
	}
	if credential.ExpiresAt != "" && !validTimestamp(credential.ExpiresAt) {
		return errors.New("订阅 OAuth token 到期时间无效")
	}
	if persisted {
		if !validCredentialID(credential.ID) {
			return errors.New("订阅凭据缺少稳定 ID")
		}
		if !validTimestamp(credential.CreatedAt) || !validTimestamp(credential.UpdatedAt) {
			return errors.New("订阅凭据时间戳无效")
		}
	}
	return nil
}

func validText(value string, max int) bool {
	return utf8.ValidString(value) && len(value) <= max && !containsControl(value)
}

func validSecret(value string, max int) bool {
	return utf8.ValidString(value) && len(value) <= max && !strings.ContainsRune(value, 0)
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func validTimestamp(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func validTokenEndpoint(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Opaque == "" && parsed.Fragment == ""
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func validProvider(provider string) bool {
	switch provider {
	case ProviderGemini, ProviderGrok, ProviderKimi, ProviderKiro:
		return true
	default:
		return false
	}
}

func newCredentialID() (string, error) {
	var random [18]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "subauth_" + base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func validCredentialID(value string) bool {
	if !strings.HasPrefix(value, "subauth_") || len(value) < 28 || len(value) > 64 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "subauth_"))
	return err == nil && len(decoded) >= 16
}

func credentialIdentity(credential Credential) string {
	kind, value := "access", credential.AccessToken
	if credential.Subject != "" {
		kind, value = "subject", credential.Subject
	} else if credential.Email != "" {
		kind, value = "email", strings.ToLower(credential.Email)
	} else if credential.Provider == ProviderGemini && credential.ProjectID != "" {
		kind, value = "project", credential.ProjectID
	} else if credential.RefreshToken != "" {
		kind, value = "refresh", credential.RefreshToken
	} else if credential.IDToken != "" {
		kind, value = "id_token", credential.IDToken
	}
	hash := sha256.Sum256([]byte(credential.Provider + "\x00" + kind + "\x00" + value))
	return credential.Provider + ":" + hex.EncodeToString(hash[:])
}

func sortCredentials(items []StoredCredential) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority != items[j].Priority {
			return items[i].Priority < items[j].Priority
		}
		return items[i].ID < items[j].ID
	})
}

func safeCredentialFilename(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value != filepath.Base(value) || strings.ContainsAny(value, "\x00/\\") {
		return "", errors.New("订阅凭据文件名无效")
	}
	if !strings.HasSuffix(strings.ToLower(value), credentialFileSuffix) {
		value += credentialFileSuffix
	}
	if len(value) > maxFilenameBytes {
		return "", errors.New("订阅凭据文件名无效")
	}
	return value, nil
}

func rejectSymlinkPathComponents(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errors.New("订阅凭据库路径无效")
	}
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
			return errors.New("检查订阅凭据库路径失败")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if !darwinSystemPathAlias(current) {
				return errors.New("订阅凭据库路径不安全")
			}
		} else if current != absolute && !info.IsDir() {
			return errors.New("订阅凭据库路径不安全")
		}
	}
	return nil
}

// darwinSystemPathAlias is the macOS /var -> /private/var (and /tmp, /etc)
// prefix. Walking t.TempDir() hits that alias; treating it like an
// attacker-controlled parent symlink made every subscription store on macOS
// look unsafe while Windows temp dirs kept passing.
func darwinSystemPathAlias(linkPath string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		return false
	}
	linkPath = filepath.Clean(linkPath)
	resolved = filepath.Clean(resolved)
	const privatePrefix = "/private"
	if resolved != privatePrefix && !strings.HasPrefix(resolved, privatePrefix+"/") {
		return false
	}
	return strings.TrimPrefix(resolved, privatePrefix) == linkPath
}

func syncDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	file, err := os.Open(dir)
	if err == nil {
		_ = file.Sync()
		_ = file.Close()
	}
	return nil
}
