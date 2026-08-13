package codexauth

import (
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
	"sort"
	"strings"
	"sync"
	"time"

	"autoto/internal/secrets"
)

const (
	DefaultBaseURL       = "https://chatgpt.com/backend-api/codex"
	DefaultProviderName  = "codex"
	DefaultModel         = "gpt-5.5"
	maxCredentialBytes   = 1 << 20
	credentialDirMode    = 0o700
	credentialFileMode   = 0o600
	credentialFileSuffix = ".json"
	DefaultPriority      = 100
	maxAliasBytes        = 200
	maxPriority          = 1_000_000
)

// Credential is the normalized on-disk representation used by Autoto's native
// Codex provider. Token fields must never be serialized into API responses or
// logs; AccountSummary is the public representation.
type Credential struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Alias        string `json:"alias,omitempty"`
	Priority     int    `json:"priority"`
	Disabled     bool   `json:"disabled,omitempty"`
	WebSockets   bool   `json:"websockets,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Email        string `json:"email,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
	PlanType     string `json:"plan_type,omitempty"`
	Expired      string `json:"expired,omitempty"`
	LastRefresh  string `json:"last_refresh,omitempty"`
}

type StoredCredential struct {
	Filename   string
	Credential Credential
}

type ImportDocument struct {
	Filename string
	Content  []byte
}

type ImportResult struct {
	Imported int
	Skipped  int
	Files    []string
}

type AccountSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Alias       string `json:"alias,omitempty"`
	Priority    int    `json:"priority"`
	Provider    string `json:"provider"`
	Email       string `json:"email,omitempty"`
	AccountID   string `json:"account_id,omitempty"`
	PlanType    string `json:"plan_type,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	Disabled    bool   `json:"disabled"`
	Refreshable bool   `json:"refreshable"`
	AuthKind    string `json:"authKind"`
}

type MetadataUpdate struct {
	Alias    *string
	Priority *int
	Disabled *bool
}

type BatchMutationResult struct {
	ID      string
	Item    StoredCredential
	Deleted bool
	Err     error
}

type QuotaSnapshot struct {
	PlanType             string                `json:"plan_type,omitempty"`
	PrimaryWindow        *RateLimitWindow      `json:"primary_window,omitempty"`
	SecondaryWindow      *RateLimitWindow      `json:"secondary_window,omitempty"`
	AdditionalRateLimits []AdditionalRateLimit `json:"additional_rate_limits,omitempty"`
	Credits              *CreditBalance        `json:"credits,omitempty"`
	RateLimitReachedType string                `json:"rate_limit_reached_type,omitempty"`
	FetchedAt            string                `json:"fetched_at,omitempty"`
}

type RateLimitWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds,omitempty"`
	ResetAt            string  `json:"reset_at,omitempty"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds,omitempty"`
}

type AdditionalRateLimit struct {
	Name            string           `json:"name,omitempty"`
	Model           string           `json:"model,omitempty"`
	PrimaryWindow   *RateLimitWindow `json:"primary_window,omitempty"`
	SecondaryWindow *RateLimitWindow `json:"secondary_window,omitempty"`
}

type CreditBalance struct {
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited,omitempty"`
	Balance    float64 `json:"balance,omitempty"`
}

type Store struct {
	dir    string
	lock   *sync.RWMutex
	cipher *secrets.CredentialCipher
}

var credentialStoreLocks sync.Map

func DefaultStoreDir(homeDir string) string {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return ""
	}
	return filepath.Join(homeDir, "credentials", "codex")
}

func NewStore(dir string) *Store {
	dir = strings.TrimSpace(dir)
	if dir != "" {
		if canonical, err := canonicalStorePath(dir); err == nil {
			dir = canonical
		}
	}
	key := filepath.Clean(dir)
	if dir == "" {
		key = ""
	}
	lockValue, _ := credentialStoreLocks.LoadOrStore(key, &sync.RWMutex{})
	var cipher *secrets.CredentialCipher
	if dir != "" {
		cipher = secrets.NewCredentialCipher(DefaultProviderName, credentialKeyPath(dir))
	}
	return &Store{dir: dir, lock: lockValue.(*sync.RWMutex), cipher: cipher}
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

func (s *Store) Configured() bool {
	credentials, err := s.Load()
	if err != nil {
		return false
	}
	for _, item := range credentials {
		credential := item.Credential
		if !credential.Disabled && (credential.AccessToken != "" || credential.RefreshToken != "") {
			return true
		}
	}
	return false
}

func (s *Store) ListAccounts() ([]AccountSummary, error) {
	credentials, err := s.Load()
	if err != nil {
		return nil, err
	}
	accounts := make([]AccountSummary, 0, len(credentials))
	for _, item := range credentials {
		accounts = append(accounts, Summary(item))
	}
	return accounts, nil
}

func Summary(item StoredCredential) AccountSummary {
	credential := item.Credential
	authKind := "access_only"
	if credential.RefreshToken != "" {
		authKind = "oauth"
	}
	return AccountSummary{
		ID:          credential.ID,
		Name:        item.Filename,
		Alias:       publicCredentialMetadata(credential.Alias, credential),
		Priority:    credential.Priority,
		Provider:    DefaultProviderName,
		Email:       publicCredentialMetadata(credential.Email, credential),
		AccountID:   publicCredentialMetadata(credential.AccountID, credential),
		PlanType:    publicCredentialMetadata(credential.PlanType, credential),
		ExpiresAt:   credential.Expired,
		Disabled:    credential.Disabled,
		Refreshable: credential.RefreshToken != "",
		AuthKind:    authKind,
	}
}

func publicCredentialMetadata(value string, credential Credential) string {
	for _, secret := range []string{credential.AccessToken, credential.RefreshToken, credential.IDToken} {
		if secret != "" && strings.Contains(value, secret) {
			return ""
		}
	}
	return value
}

func (s *Store) Load() ([]StoredCredential, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil, errors.New("Codex 本地凭据库路径未配置")
	}
	// Loading may migrate legacy credentials by assigning a stable opaque ID and
	// default priority, so it intentionally takes the exclusive store lock.
	s.lock.Lock()
	defer s.lock.Unlock()
	return s.loadLocked()
}

func (s *Store) Import(documents []ImportDocument) (ImportResult, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return ImportResult{}, errors.New("Codex 本地凭据库路径未配置")
	}
	if len(documents) == 0 {
		return ImportResult{}, errors.New("没有可导入的 Codex 凭据")
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	if err := s.ensureDirLocked(); err != nil {
		return ImportResult{}, err
	}
	existing, err := s.loadLocked()
	if err != nil {
		return ImportResult{}, err
	}
	byIdentity := make(map[string]StoredCredential, len(existing))
	usedNames := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		byIdentity[credentialIdentity(item.Credential)] = item
		usedNames[item.Filename] = struct{}{}
	}

	result := ImportResult{Files: make([]string, 0, len(documents))}
	for index, document := range documents {
		credential, err := decodeCredential(document.Content)
		if err != nil {
			return ImportResult{}, fmt.Errorf("第 %d 个账号无法保存：凭据内容无效", index+1)
		}
		identity := credentialIdentity(credential)
		current, ok := byIdentity[identity]
		if ok {
			// Authentication material may rotate, but management identity and user
			// metadata belong to the existing local account record.
			credential.ID = current.Credential.ID
			credential.Alias = current.Credential.Alias
			credential.Priority = current.Credential.Priority
			credential.Disabled = current.Credential.Disabled
			if credentialFingerprint(current.Credential) == credentialFingerprint(credential) {
				result.Skipped++
				continue
			}
			if err := s.writeCredentialLocked(current.Filename, credential); err != nil {
				return ImportResult{}, fmt.Errorf("第 %d 个账号保存失败：%w", index+1, err)
			}
			current.Credential = credential
			byIdentity[identity] = current
			result.Imported++
			result.Files = append(result.Files, current.Filename)
			continue
		}

		credential.ID, err = newCredentialID()
		if err != nil {
			return ImportResult{}, fmt.Errorf("第 %d 个账号无法分配本地 ID", index+1)
		}
		if credential.Priority == 0 {
			credential.Priority = DefaultPriority
		}
		filename := uniqueCredentialFilename(document.Filename, identity, usedNames)
		if err := s.writeCredentialLocked(filename, credential); err != nil {
			return ImportResult{}, fmt.Errorf("第 %d 个账号保存失败：%w", index+1, err)
		}
		stored := StoredCredential{Filename: filename, Credential: credential}
		byIdentity[identity] = stored
		usedNames[filename] = struct{}{}
		result.Imported++
		result.Files = append(result.Files, filename)
	}
	return result, nil
}

func (s *Store) Update(item StoredCredential) error {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return errors.New("Codex 本地凭据库路径未配置")
	}
	id := strings.TrimSpace(item.Credential.ID)
	if id == "" {
		return os.ErrNotExist
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	items, err := s.loadLocked()
	if err != nil {
		return err
	}
	for _, current := range items {
		if current.Credential.ID != id {
			continue
		}
		// Runtime token refreshes may update authentication and provider metadata,
		// but they must not replace the stable management identity or user choices.
		item.Credential.ID = current.Credential.ID
		item.Credential.Alias = current.Credential.Alias
		item.Credential.Priority = current.Credential.Priority
		item.Credential.Disabled = current.Credential.Disabled
		if err := validateCredential(item.Credential); err != nil {
			return err
		}
		return s.writeCredentialLocked(current.Filename, item.Credential)
	}
	return os.ErrNotExist
}

func (s *Store) GetByID(id string) (StoredCredential, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return StoredCredential{}, os.ErrNotExist
	}
	items, err := s.Load()
	if err != nil {
		return StoredCredential{}, err
	}
	for _, item := range items {
		if item.Credential.ID == id {
			return item, nil
		}
	}
	return StoredCredential{}, os.ErrNotExist
}

// ExportByID returns a complete re-importable credential document. The content
// contains token material and must only be sent through an explicitly guarded
// download path; callers must never log it or include it in list responses.
func (s *Store) ExportByID(id string) (ImportDocument, error) {
	item, err := s.GetByID(id)
	if err != nil {
		return ImportDocument{}, err
	}
	data, err := json.MarshalIndent(item.Credential, "", "  ")
	if err != nil {
		return ImportDocument{}, errors.New("序列化 Codex 凭据失败")
	}
	data = append(data, '\n')
	if len(data) > maxCredentialBytes {
		return ImportDocument{}, errors.New("Codex 凭据导出内容过大")
	}
	return ImportDocument{Filename: item.Filename, Content: data}, nil
}

func (s *Store) UpdateMetadata(id string, update MetadataUpdate) (StoredCredential, error) {
	results, err := s.BatchUpdateMetadata([]string{id}, update)
	if err != nil {
		return StoredCredential{}, err
	}
	if len(results) != 1 {
		return StoredCredential{}, os.ErrNotExist
	}
	return results[0].Item, results[0].Err
}

// BatchUpdateMetadata holds the store lock once and loads the credential
// directory once. Each credential is still persisted independently; callers
// must not treat the returned results as a cross-file transaction.
func (s *Store) BatchUpdateMetadata(ids []string, update MetadataUpdate) ([]BatchMutationResult, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil, errors.New("Codex 本地凭据库路径未配置")
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	items, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]StoredCredential, len(items))
	for _, item := range items {
		byID[item.Credential.ID] = item
	}
	results := make([]BatchMutationResult, 0, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		result := BatchMutationResult{ID: id}
		item, ok := byID[id]
		if id == "" || !ok {
			result.Err = os.ErrNotExist
			results = append(results, result)
			continue
		}
		if update.Alias != nil {
			item.Credential.Alias = strings.TrimSpace(*update.Alias)
		}
		if update.Priority != nil {
			item.Credential.Priority = *update.Priority
		}
		if update.Disabled != nil {
			item.Credential.Disabled = *update.Disabled
		}
		if err := validateCredential(item.Credential); err != nil {
			result.Err = err
			results = append(results, result)
			continue
		}
		if err := s.writeCredentialLocked(item.Filename, item.Credential); err != nil {
			result.Err = err
			results = append(results, result)
			continue
		}
		result.Item = item
		byID[id] = item
		results = append(results, result)
	}
	return results, nil
}

func (s *Store) Disable(id string) error {
	disabled := true
	_, err := s.UpdateMetadata(id, MetadataUpdate{Disabled: &disabled})
	return err
}

func (s *Store) Delete(id string) error {
	results, err := s.BatchDelete([]string{id})
	if err != nil {
		return err
	}
	if len(results) != 1 {
		return os.ErrNotExist
	}
	return results[0].Err
}

// BatchDelete holds the store lock once and loads the credential directory
// once. Files are removed independently and the directory is synced once after
// all removals; this is not atomic with external database cleanup.
func (s *Store) BatchDelete(ids []string) ([]BatchMutationResult, error) {
	if s == nil || strings.TrimSpace(s.dir) == "" {
		return nil, errors.New("Codex 本地凭据库路径未配置")
	}
	s.lock.Lock()
	defer s.lock.Unlock()
	items, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]StoredCredential, len(items))
	for _, item := range items {
		byID[item.Credential.ID] = item
	}
	results := make([]BatchMutationResult, 0, len(ids))
	removedIndexes := make([]int, 0, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		result := BatchMutationResult{ID: id}
		item, ok := byID[id]
		if id == "" || !ok {
			result.Err = os.ErrNotExist
			results = append(results, result)
			continue
		}
		filename, err := safeCredentialFilename(item.Filename)
		if err != nil {
			result.Err = err
			results = append(results, result)
			continue
		}
		path := filepath.Join(s.dir, filename)
		info, err := os.Lstat(path)
		if err != nil {
			result.Err = err
			results = append(results, result)
			continue
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			result.Err = errors.New("Codex 凭据目标路径不安全")
			results = append(results, result)
			continue
		}
		if err := os.Remove(path); err != nil {
			result.Err = errors.New("删除 Codex 凭据失败")
			results = append(results, result)
			continue
		}
		result.Item = item
		result.Deleted = true
		delete(byID, id)
		removedIndexes = append(removedIndexes, len(results))
		results = append(results, result)
	}
	if len(removedIndexes) > 0 {
		if err := syncDirectory(s.dir); err != nil {
			for _, index := range removedIndexes {
				results[index].Err = err
			}
		}
	}
	return results, nil
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
		return nil, errors.New("Codex 本地凭据库路径不安全")
	}
	if err := os.Chmod(s.dir, credentialDirMode); err != nil {
		return nil, errors.New("设置 Codex 本地凭据库权限失败")
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, errors.New("读取 Codex 本地凭据库失败")
	}
	items := make([]StoredCredential, 0, len(entries))
	usedIDs := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(strings.ToLower(entry.Name()), credentialFileSuffix) {
			continue
		}
		filename, err := safeCredentialFilename(entry.Name())
		if err != nil {
			continue
		}
		path := filepath.Join(s.dir, filename)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("读取 Codex 凭据 %s 失败", filename)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(data) > maxCredentialBytes {
			return nil, fmt.Errorf("读取 Codex 凭据 %s 失败", filename)
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
		if err != nil {
			return nil, fmt.Errorf("Codex 凭据 %s 已损坏", filename)
		}
		if !validCredentialID(credential.ID) {
			credential.ID, err = newCredentialID()
			if err != nil {
				return nil, errors.New("无法迁移 Codex 凭据 ID")
			}
			migrated = true
		}
		if _, duplicate := usedIDs[credential.ID]; duplicate {
			credential.ID, err = newCredentialID()
			if err != nil {
				return nil, errors.New("无法迁移重复的 Codex 凭据 ID")
			}
			migrated = true
		}
		usedIDs[credential.ID] = struct{}{}
		if credential.Priority == 0 {
			credential.Priority = DefaultPriority
			migrated = true
		}
		if migrated {
			if err := s.writeCredentialLocked(filename, credential); err != nil {
				return nil, fmt.Errorf("迁移 Codex 凭据 %s 失败", filename)
			}
		} else if err := os.Chmod(path, credentialFileMode); err != nil {
			return nil, fmt.Errorf("设置 Codex 凭据 %s 权限失败", filename)
		}
		items = append(items, StoredCredential{Filename: filename, Credential: credential})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Filename < items[j].Filename })
	return items, nil
}

func (s *Store) ensureDirLocked() error {
	if err := rejectSymlinkPathComponents(s.dir); err != nil {
		return err
	}
	parent := filepath.Dir(s.dir)
	if parent != "." && parent != s.dir {
		if err := os.MkdirAll(parent, credentialDirMode); err != nil {
			return errors.New("创建 Codex 本地凭据库失败")
		}
	}
	if info, err := os.Lstat(s.dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Codex 本地凭据库路径不安全")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("检查 Codex 本地凭据库失败")
	} else if err := os.Mkdir(s.dir, credentialDirMode); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("创建 Codex 本地凭据库失败")
	}
	if err := os.Chmod(s.dir, credentialDirMode); err != nil {
		return errors.New("设置 Codex 本地凭据库权限失败")
	}
	return nil
}

func (s *Store) writeCredentialLocked(filename string, credential Credential) error {
	filename, err := safeCredentialFilename(filename)
	if err != nil {
		return err
	}
	if err := validateCredential(credential); err != nil {
		return err
	}
	if !validCredentialID(credential.ID) || credential.Priority <= 0 {
		return errors.New("Codex 凭据缺少稳定 ID 或有效优先级")
	}
	data, err := json.MarshalIndent(credential, "", "  ")
	if err != nil {
		return errors.New("序列化 Codex 凭据失败")
	}
	data = append(data, '\n')
	data, err = s.cipher.Encrypt(data)
	if err != nil || len(data) > maxCredentialBytes {
		return errors.New("加密 Codex 凭据失败")
	}

	temp, err := os.CreateTemp(s.dir, ".codex-credential-*")
	if err != nil {
		return errors.New("创建 Codex 凭据临时文件失败")
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
		return errors.New("设置 Codex 凭据权限失败")
	}
	if _, err := temp.Write(data); err != nil {
		return errors.New("写入 Codex 凭据失败")
	}
	if err := temp.Sync(); err != nil {
		return errors.New("同步 Codex 凭据失败")
	}
	if err := temp.Close(); err != nil {
		return errors.New("关闭 Codex 凭据文件失败")
	}
	target := filepath.Join(s.dir, filename)
	if info, err := os.Lstat(target); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("Codex 凭据目标路径不安全")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("检查 Codex 凭据目标失败")
	}
	if err := os.Rename(tempName, target); err != nil {
		// Windows cannot rename onto an existing file; remove then rename. The
		// earlier Lstat already rejected non-regular/symlink targets.
		if runtime.GOOS == "windows" {
			if removeErr := os.Remove(target); removeErr == nil {
				err = os.Rename(tempName, target)
			}
		}
		if err != nil {
			return errors.New("保存 Codex 凭据失败")
		}
	}
	if err := os.Chmod(target, credentialFileMode); err != nil {
		return errors.New("设置 Codex 凭据权限失败")
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
	if err := decoder.Decode(&credential); err != nil {
		return Credential{}, errors.New("invalid credential")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Credential{}, errors.New("invalid credential")
	}
	credential.ID = strings.TrimSpace(credential.ID)
	credential.Type = strings.ToLower(strings.TrimSpace(credential.Type))
	if credential.Type == "" {
		credential.Type = DefaultProviderName
	}
	credential.Alias = strings.TrimSpace(credential.Alias)
	credential.AccessToken = strings.TrimSpace(credential.AccessToken)
	credential.RefreshToken = strings.TrimSpace(credential.RefreshToken)
	credential.IDToken = strings.TrimSpace(credential.IDToken)
	credential.Email = strings.TrimSpace(credential.Email)
	credential.AccountID = strings.TrimSpace(credential.AccountID)
	credential.PlanType = strings.TrimSpace(credential.PlanType)
	credential.Expired = strings.TrimSpace(credential.Expired)
	credential.LastRefresh = strings.TrimSpace(credential.LastRefresh)
	if err := validateCredential(credential); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

func validateCredential(credential Credential) error {
	if credential.Type != "" && credential.Type != DefaultProviderName {
		return errors.New("凭据类型不是 Codex")
	}
	if len(credential.Alias) > maxAliasBytes {
		return fmt.Errorf("Codex 账号别名不能超过 %d 字节", maxAliasBytes)
	}
	if credential.Priority < 0 || credential.Priority > maxPriority {
		return fmt.Errorf("Codex 账号优先级必须在 1 到 %d 之间", maxPriority)
	}
	if credential.AccessToken == "" && credential.RefreshToken == "" {
		return errors.New("Codex 凭据缺少 access_token 或 refresh_token")
	}
	for _, field := range []string{credential.ID, credential.Alias, credential.AccessToken, credential.RefreshToken, credential.IDToken, credential.Email, credential.AccountID, credential.PlanType, credential.Expired, credential.LastRefresh} {
		if strings.ContainsRune(field, 0) {
			return errors.New("Codex 凭据包含无效字符")
		}
	}
	return nil
}

func newCredentialID() (string, error) {
	var random [18]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "codex_" + base64.RawURLEncoding.EncodeToString(random[:]), nil
}

// ValidCredentialID reports whether value is a canonical opaque Codex account ID.
func ValidCredentialID(value string) bool {
	return validCredentialID(strings.TrimSpace(value))
}

func validCredentialID(value string) bool {
	if !strings.HasPrefix(value, "codex_") || len(value) < 20 || len(value) > 64 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "codex_"))
	return err == nil
}

func credentialIdentity(credential Credential) string {
	if accountID := strings.ToLower(strings.TrimSpace(credential.AccountID)); accountID != "" {
		return "account:" + accountID
	}
	if email := strings.ToLower(strings.TrimSpace(credential.Email)); email != "" {
		return "email:" + email
	}
	hash := sha256.Sum256([]byte(credential.AccessToken + "\x00" + credential.RefreshToken))
	return "token:" + hex.EncodeToString(hash[:12])
}

func credentialFingerprint(credential Credential) string {
	data, _ := json.Marshal(credential)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func publicCredentialID(identity string) string {
	hash := sha256.Sum256([]byte(identity))
	return "codex_" + hex.EncodeToString(hash[:8])
}

func uniqueCredentialFilename(requested, identity string, used map[string]struct{}) string {
	base, err := safeCredentialFilename(requested)
	if err != nil {
		base = "autoto-codex" + credentialFileSuffix
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" {
		stem = "autoto-codex"
	}
	candidate := stem + credentialFileSuffix
	if _, exists := used[candidate]; !exists {
		return candidate
	}
	hash := sha256.Sum256([]byte(identity))
	candidate = fmt.Sprintf("%s-%s%s", stem, hex.EncodeToString(hash[:4]), credentialFileSuffix)
	if _, exists := used[candidate]; !exists {
		return candidate
	}
	for index := 2; ; index++ {
		candidate = fmt.Sprintf("%s-%s-%d%s", stem, hex.EncodeToString(hash[:4]), index, credentialFileSuffix)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func canonicalStorePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := absolute
	missing := make([]string, 0, 4)
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("no existing credential path ancestor")
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func rejectSymlinkPathComponents(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errors.New("Codex 本地凭据库路径无效")
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
			return errors.New("检查 Codex 本地凭据库路径失败")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Codex 本地凭据库路径不安全")
		}
		if current != absolute && !info.IsDir() {
			return errors.New("Codex 本地凭据库路径不安全")
		}
	}
	return nil
}

func safeCredentialFilename(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == string(filepath.Separator) || value != filepath.Base(value) || strings.ContainsAny(value, "\x00/\\") {
		return "", errors.New("Codex 凭据文件名无效")
	}
	if !strings.HasSuffix(strings.ToLower(value), credentialFileSuffix) {
		value += credentialFileSuffix
	}
	if len(value) > 160 {
		return "", errors.New("Codex 凭据文件名无效")
	}
	return value, nil
}

func syncDirectory(dir string) error {
	// Windows does not support fsync on a directory handle, so skip it there;
	// durability relies on the file sync plus rename.
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

func (c Credential) ExpiresAt() time.Time {
	value := strings.TrimSpace(c.Expired)
	if value == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC()
	}
	return time.Time{}
}
