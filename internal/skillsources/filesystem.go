// Package skillsources discovers untrusted Agent Skills from fixed, read-only
// filesystem locations. It does not persist, enable, or execute discovered data.
package skillsources

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"autoto/internal/skills"
)

const (
	DefaultMaxCandidates       = 256
	DefaultMaxFileBytes        = skills.MaxContentBytes
	DefaultMaxSidecarBytes     = skills.MaxOpenAISidecarBytes
	DefaultMaxTotalBytes       = 8 * 1024 * 1024
	DefaultMaxDepth            = 5
	DefaultMaxDirectoryEntries = 1024
	MaxDiagnosticMessageBytes  = 240
)

const (
	ConflictNone    = "none"
	ConflictCommand = "command_conflict"
)

// Adapter describes one exact, root-relative Agent Skills directory.
type Adapter struct {
	ID                string `json:"id"`
	RelativeDirectory string `json:"relativeDirectory"`
	Rank              int    `json:"rank"`
	Compatibility     bool   `json:"compatibility"`
}

var defaultAdapters = []Adapter{
	{ID: "agents", RelativeDirectory: ".agents/skills", Rank: 0},
	{ID: "claude", RelativeDirectory: ".claude/skills", Rank: 1},
	{ID: "gemini", RelativeDirectory: ".gemini/skills", Rank: 2},
	{ID: "kimi-code", RelativeDirectory: ".kimi-code/skills", Rank: 3},
	{ID: "codex", RelativeDirectory: ".codex/skills", Rank: 4, Compatibility: true},
	{ID: "kimi", RelativeDirectory: ".kimi/skills", Rank: 5, Compatibility: true},
}

// DefaultAdapters returns a copy in deterministic precedence order.
func DefaultAdapters() []Adapter {
	return append([]Adapter(nil), defaultAdapters...)
}

// Limits bounds all discovery work and bytes read from untrusted trees.
type Limits struct {
	MaxCandidates       int
	MaxFileBytes        int64
	MaxSidecarBytes     int64
	MaxTotalBytes       int64
	MaxDepth            int
	MaxDirectoryEntries int
}

// Options customizes discovery. Zero fields receive secure defaults.
type Options struct {
	Adapters []Adapter
	Limits   Limits
}

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Diagnostic contains no source excerpt because source files are untrusted and
// may contain secrets.
type Diagnostic struct {
	Path        string   `json:"path,omitempty"`
	Adapter     string   `json:"adapter,omitempty"`
	AdapterRank int      `json:"adapterRank"`
	Code        string   `json:"code"`
	Severity    Severity `json:"severity"`
	Message     string   `json:"message"`
}

// Provenance identifies a candidate without exposing the absolute discovery
// root. RootID is a stable SHA-256 identifier scoped to this filesystem root.
type Provenance struct {
	Kind         string `json:"kind"`
	RootID       string `json:"rootId"`
	AdapterID    string `json:"adapterId"`
	AdapterRank  int    `json:"adapterRank"`
	RelativePath string `json:"relativePath"`
}

// Conflict records an unresolved same-scope, same-rank command collision. No
// candidate is silently selected as the winner.
type Conflict struct {
	Scope          string   `json:"scope"`
	Command        string   `json:"command"`
	AdapterRank    int      `json:"adapterRank"`
	CandidatePaths []string `json:"candidatePaths"`
}

// Candidate is a normalized and scanned import candidate. Hash is the existing
// skills.Hash value; source hashes identify the exact bytes read.
type Candidate struct {
	Adapter             Adapter           `json:"adapter"`
	AdapterRank         int               `json:"adapterRank"`
	Provenance          Provenance        `json:"provenance"`
	RelativePath        string            `json:"relativePath"`
	SidecarRelativePath string            `json:"sidecarRelativePath,omitempty"`
	SourceHash          string            `json:"sourceHash"`
	SidecarSourceHash   string            `json:"sidecarSourceHash,omitempty"`
	Hash                string            `json:"hash"`
	ConflictStatus      string            `json:"conflictStatus"`
	Skill               skills.Skill      `json:"skill"`
	Scan                skills.ScanResult `json:"scan"`
	Diagnostics         []Diagnostic      `json:"diagnostics,omitempty"`
}

// Result is deterministic: candidates and diagnostics are sorted first by
// adapter rank and then by a case-folded safe relative path.
type Result struct {
	Candidates  []Candidate  `json:"candidates"`
	Conflicts   []Conflict   `json:"conflicts,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	Truncated   bool         `json:"truncated"`
	BytesRead   int64        `json:"bytesRead"`
}

// FileSource is a read-only source rooted at one project or home directory.
type FileSource struct {
	root     string
	rootID   string
	adapters []Adapter
	limits   Limits
}

// NewFileSource validates the root without following a symlink or junction to a
// different path.
func NewFileSource(root string, options ...Options) (*FileSource, error) {
	if len(options) > 1 {
		return nil, errors.New("at most one skillsource Options value is allowed")
	}
	opts := Options{}
	if len(options) == 1 {
		opts = options[0]
	}
	absolute, err := secureRoot(root)
	if err != nil {
		return nil, err
	}
	adapters := opts.Adapters
	if len(adapters) == 0 {
		adapters = DefaultAdapters()
	} else {
		adapters = append([]Adapter(nil), adapters...)
	}
	if err := validateAdapters(adapters); err != nil {
		return nil, err
	}
	sort.SliceStable(adapters, func(i, j int) bool {
		if adapters[i].Rank != adapters[j].Rank {
			return adapters[i].Rank < adapters[j].Rank
		}
		return safeRelativeLess(adapters[i].RelativeDirectory, adapters[j].RelativeDirectory)
	})
	return &FileSource{root: absolute, rootID: rootProvenanceID(absolute), adapters: adapters, limits: normalizeLimits(opts.Limits)}, nil
}

// Discover is a convenience wrapper around FileSource.Discover.
func Discover(root string, options ...Options) (Result, error) {
	source, err := NewFileSource(root, options...)
	if err != nil {
		return Result{}, err
	}
	return source.Discover()
}

// Discover examines only <adapter>/<skill>/SKILL.md and the optional
// <adapter>/<skill>/agents/openai.yaml sidecar.
func (source *FileSource) Discover() (Result, error) {
	if source == nil {
		return Result{}, errors.New("skillsource is nil")
	}
	result := Result{Candidates: []Candidate{}, Conflicts: []Conflict{}}
	considered := 0
	stop := false
	for _, adapter := range source.adapters {
		if stop {
			break
		}
		adapterDepth := pathDepth(adapter.RelativeDirectory)
		if adapterDepth+2 > source.limits.MaxDepth {
			result.Diagnostics = append(result.Diagnostics, diagnostic(adapter, adapter.RelativeDirectory, "depth_limit", SeverityWarning, "Adapter exceeds the configured discovery depth."))
			result.Truncated = true
			continue
		}
		adapterPath := filepath.Join(source.root, filepath.FromSlash(adapter.RelativeDirectory))
		if exactErr := verifyExactDirectory(source.root, adapter.RelativeDirectory, source.limits.MaxDirectoryEntries); exactErr != nil {
			if errors.Is(exactErr, os.ErrNotExist) {
				continue
			}
			result.Diagnostics = append(result.Diagnostics, diagnostic(adapter, adapter.RelativeDirectory, errorCode(exactErr), SeverityWarning, safeErrorMessage(exactErr)))
			continue
		}
		entries, truncated, readErr := readSecureDirectory(source.root, adapterPath, source.limits.MaxDirectoryEntries)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			result.Diagnostics = append(result.Diagnostics, diagnostic(adapter, adapter.RelativeDirectory, errorCode(readErr), SeverityWarning, safeErrorMessage(readErr)))
			continue
		}
		if truncated {
			result.Diagnostics = append(result.Diagnostics, diagnostic(adapter, adapter.RelativeDirectory, "directory_entry_limit", SeverityWarning, "Adapter directory entry limit reached."))
			result.Truncated = true
		}
		sort.Slice(entries, func(i, j int) bool {
			left, right := strings.ToLower(entries[i].Name()), strings.ToLower(entries[j].Name())
			if left != right {
				return left < right
			}
			return entries[i].Name() < entries[j].Name()
		})

		for i := 0; i < len(entries); {
			folded := strings.ToLower(entries[i].Name())
			j := i + 1
			for j < len(entries) && strings.ToLower(entries[j].Name()) == folded {
				j++
			}
			if j-i > 1 {
				for _, entry := range entries[i:j] {
					rel := joinRelative(adapter.RelativeDirectory, entry.Name())
					result.Diagnostics = append(result.Diagnostics, diagnostic(adapter, rel, "case_conflict", SeverityError, "Case-insensitive skill directory collision was rejected."))
				}
				i = j
				continue
			}
			entry := entries[i]
			i = j
			if entry.Type()&os.ModeSymlink != 0 {
				rel := joinRelative(adapter.RelativeDirectory, entry.Name())
				result.Diagnostics = append(result.Diagnostics, diagnostic(adapter, rel, "symlink_rejected", SeverityError, "Symlinked skill directory was rejected."))
				continue
			}
			info, infoErr := entry.Info()
			if infoErr != nil || !info.IsDir() {
				continue
			}
			considered++
			if considered > source.limits.MaxCandidates {
				result.Diagnostics = append(result.Diagnostics, diagnostic(adapter, adapter.RelativeDirectory, "candidate_limit", SeverityWarning, "Skill candidate limit reached."))
				result.Truncated = true
				stop = true
				break
			}
			candidate, candidateDiagnostics, bytesRead := source.readCandidate(adapter, entry.Name(), result.BytesRead)
			result.BytesRead += bytesRead
			result.Diagnostics = append(result.Diagnostics, candidateDiagnostics...)
			if candidate != nil {
				candidate.Diagnostics = append(candidate.Diagnostics, candidateDiagnostics...)
				result.Candidates = append(result.Candidates, *candidate)
			}
			if diagnosticsContainCode(candidateDiagnostics, "total_byte_limit") {
				result.Truncated = true
				stop = true
				break
			}
			if result.BytesRead >= source.limits.MaxTotalBytes {
				result.Diagnostics = append(result.Diagnostics, diagnostic(adapter, adapter.RelativeDirectory, "total_byte_limit", SeverityWarning, "Total skill source byte limit reached."))
				result.Truncated = true
				stop = true
				break
			}
		}
	}
	markCommandConflicts(&result, source.rootID)
	sortResult(&result)
	return result, nil
}

func (source *FileSource) readCandidate(adapter Adapter, directoryName string, alreadyRead int64) (*Candidate, []Diagnostic, int64) {
	candidateDir := filepath.Join(source.root, filepath.FromSlash(adapter.RelativeDirectory), directoryName)
	skillRelative := joinRelative(adapter.RelativeDirectory, directoryName, "SKILL.md")
	diagnostics := []Diagnostic{}
	if _, err := exactChild(candidateDir, "SKILL.md", source.limits.MaxDirectoryEntries); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			diagnostics = append(diagnostics, diagnostic(adapter, skillRelative, errorCode(err), SeverityError, safeErrorMessage(err)))
		}
		return nil, diagnostics, 0
	}
	remaining := source.limits.MaxTotalBytes - alreadyRead
	content, readBytes, err := readSecureFile(source.root, filepath.Join(candidateDir, "SKILL.md"), source.limits.MaxFileBytes, remaining)
	if err != nil {
		diagnostics = append(diagnostics, diagnostic(adapter, skillRelative, errorCode(err), SeverityError, safeErrorMessage(err)))
		return nil, diagnostics, readBytes
	}
	document, err := skills.ParseAgentSkillForDirectory(string(content), directoryName)
	if err != nil {
		diagnostics = append(diagnostics, diagnostic(adapter, skillRelative, "invalid_skill", SeverityError, "SKILL.md failed strict Agent Skill validation."))
		return nil, diagnostics, readBytes
	}
	for _, item := range document.Diagnostics {
		diagnostics = append(diagnostics, diagnostic(adapter, skillRelative, item.Code, SeverityWarning, item.Message))
	}

	var sidecar *skills.OpenAISidecar
	sidecarRelative := ""
	sidecarSourceHash := ""
	if pathDepth(adapter.RelativeDirectory)+3 <= source.limits.MaxDepth {
		agentsDir := filepath.Join(candidateDir, "agents")
		if _, agentsErr := exactChild(candidateDir, "agents", source.limits.MaxDirectoryEntries); agentsErr == nil {
			if _, sidecarErr := exactChild(agentsDir, "openai.yaml", source.limits.MaxDirectoryEntries); sidecarErr == nil {
				sidecarRelative = joinRelative(adapter.RelativeDirectory, directoryName, "agents", "openai.yaml")
				remaining = source.limits.MaxTotalBytes - alreadyRead - readBytes
				sidecarContent, sidecarBytes, sidecarReadErr := readSecureFile(source.root, filepath.Join(agentsDir, "openai.yaml"), source.limits.MaxSidecarBytes, remaining)
				readBytes += sidecarBytes
				if sidecarReadErr != nil {
					diagnostics = append(diagnostics, diagnostic(adapter, sidecarRelative, errorCode(sidecarReadErr), SeverityWarning, safeErrorMessage(sidecarReadErr)))
					sidecarRelative = ""
				} else {
					sidecarSum := sha256.Sum256(sidecarContent)
					sidecarSourceHash = hex.EncodeToString(sidecarSum[:])
					if parsedSidecar, parseErr := skills.ParseOpenAISidecar(string(sidecarContent)); parseErr != nil {
						diagnostics = append(diagnostics, diagnostic(adapter, sidecarRelative, "invalid_sidecar", SeverityWarning, "OpenAI sidecar failed strict static-subset validation and was ignored."))
					} else {
						sidecar = &parsedSidecar
					}
				}
			} else if !errors.Is(sidecarErr, os.ErrNotExist) {
				diagnostics = append(diagnostics, diagnostic(adapter, joinRelative(adapter.RelativeDirectory, directoryName, "agents", "openai.yaml"), errorCode(sidecarErr), SeverityWarning, safeErrorMessage(sidecarErr)))
			}
		} else if !errors.Is(agentsErr, os.ErrNotExist) {
			diagnostics = append(diagnostics, diagnostic(adapter, joinRelative(adapter.RelativeDirectory, directoryName, "agents"), errorCode(agentsErr), SeverityWarning, safeErrorMessage(agentsErr)))
		}
	} else {
		diagnostics = append(diagnostics, diagnostic(adapter, skillRelative, "depth_limit", SeverityInfo, "OpenAI sidecar discovery was skipped by the configured depth limit."))
	}

	normalized, err := skills.NormalizeAgentSkill(document, sidecar)
	if err != nil {
		diagnostics = append(diagnostics, diagnostic(adapter, skillRelative, "normalization_failed", SeverityError, "Agent Skill failed final normalization."))
		return nil, diagnostics, readBytes
	}
	scan := skills.Scan(normalized)
	sum := sha256.Sum256(content)
	return &Candidate{
		Adapter:             adapter,
		AdapterRank:         adapter.Rank,
		Provenance:          Provenance{Kind: "filesystem", RootID: source.rootID, AdapterID: adapter.ID, AdapterRank: adapter.Rank, RelativePath: skillRelative},
		RelativePath:        skillRelative,
		SidecarRelativePath: sidecarRelative,
		SourceHash:          hex.EncodeToString(sum[:]),
		SidecarSourceHash:   sidecarSourceHash,
		Hash:                scan.Hash,
		ConflictStatus:      ConflictNone,
		Skill:               normalized,
		Scan:                scan,
	}, diagnostics, readBytes
}

func normalizeLimits(limits Limits) Limits {
	if limits.MaxCandidates <= 0 {
		limits.MaxCandidates = DefaultMaxCandidates
	}
	if limits.MaxFileBytes <= 0 || limits.MaxFileBytes > skills.MaxContentBytes {
		limits.MaxFileBytes = DefaultMaxFileBytes
	}
	if limits.MaxSidecarBytes <= 0 || limits.MaxSidecarBytes > skills.MaxOpenAISidecarBytes {
		limits.MaxSidecarBytes = DefaultMaxSidecarBytes
	}
	if limits.MaxTotalBytes <= 0 {
		limits.MaxTotalBytes = DefaultMaxTotalBytes
	}
	if limits.MaxDepth <= 0 {
		limits.MaxDepth = DefaultMaxDepth
	}
	if limits.MaxDirectoryEntries <= 0 {
		limits.MaxDirectoryEntries = DefaultMaxDirectoryEntries
	}
	return limits
}

func validateAdapters(adapters []Adapter) error {
	seenIDs := make(map[string]struct{}, len(adapters))
	seenDirectories := make(map[string]struct{}, len(adapters))
	for _, adapter := range adapters {
		if err := validateAdapter(adapter); err != nil {
			return err
		}
		foldedID := strings.ToLower(adapter.ID)
		if _, exists := seenIDs[foldedID]; exists {
			return errors.New("skillsource adapters contain duplicate or case-insensitive ID conflicts")
		}
		seenIDs[foldedID] = struct{}{}
		foldedDirectory := strings.ToLower(adapter.RelativeDirectory)
		if _, exists := seenDirectories[foldedDirectory]; exists {
			return errors.New("skillsource adapters contain duplicate or case-insensitive directory conflicts")
		}
		seenDirectories[foldedDirectory] = struct{}{}
	}
	return nil
}

func validateAdapter(adapter Adapter) error {
	if strings.TrimSpace(adapter.ID) == "" {
		return errors.New("skillsource adapter ID is required")
	}
	if adapter.Rank < 0 {
		return errors.New("skillsource adapter rank must be non-negative")
	}
	relative := adapter.RelativeDirectory
	if relative == "" || strings.Contains(relative, "\\") || strings.HasPrefix(relative, "/") || path.Clean(relative) != relative {
		return errors.New("skillsource adapter has an unsafe relative directory")
	}
	for _, component := range strings.Split(relative, "/") {
		if component == "" || component == "." || component == ".." || strings.Contains(component, ":") {
			return errors.New("skillsource adapter has an unsafe relative directory")
		}
	}
	return nil
}

func secureRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("skillsource root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("skillsource root must be a real directory, not a symlink")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if !sameFilesystemPath(absolute, resolved) {
		return "", errors.New("skillsource root resolves through a symlink or junction")
	}
	return filepath.Clean(absolute), nil
}

func readSecureDirectory(root, path string, maxEntries int) ([]os.DirEntry, bool, error) {
	if err := verifyNoSymlinkPath(root, path); err != nil {
		return nil, false, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, false, err
	}
	if !before.IsDir() {
		return nil, false, &sourceError{code: "not_directory", message: "Expected skill source directory is not a directory."}
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer directory.Close()
	after, err := directory.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, false, &sourceError{code: "path_changed", message: "Skill source directory changed while being opened."}
	}
	entries := make([]os.DirEntry, 0, minInt(maxEntries, 64))
	for len(entries) <= maxEntries {
		batch, readErr := directory.ReadDir(minInt(64, maxEntries+1-len(entries)))
		entries = append(entries, batch...)
		if errors.Is(readErr, io.EOF) {
			return entries, false, nil
		}
		if readErr != nil {
			return nil, false, readErr
		}
	}
	return entries[:maxEntries], true, nil
}

func verifyExactDirectory(root, relative string, maxEntries int) error {
	current := root
	for _, component := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		entry, err := exactChild(current, component, maxEntries)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return &sourceError{code: "not_directory", message: "Expected skill source directory is not a directory."}
		}
		current = filepath.Join(current, component)
	}
	return nil
}

func exactChild(directory, expected string, maxEntries int) (os.DirEntry, error) {
	entries, truncated, err := readSecureDirectory(directory, directory, maxEntries)
	if err != nil {
		return nil, err
	}
	var exact os.DirEntry
	folded := 0
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), expected) {
			folded++
			if entry.Name() == expected {
				exact = entry
			}
		}
	}
	if folded > 1 {
		return nil, &sourceError{code: "case_conflict", message: "Case-insensitive path collision was rejected."}
	}
	if exact != nil {
		if exact.Type()&os.ModeSymlink != 0 {
			return nil, &sourceError{code: "symlink_rejected", message: "Symlinked skill source path was rejected."}
		}
		return exact, nil
	}
	if folded == 1 {
		return nil, &sourceError{code: "path_case_mismatch", message: "Skill source path casing does not match the required name."}
	}
	if truncated {
		return nil, &sourceError{code: "directory_entry_limit", message: "Directory entry limit prevented exact path verification."}
	}
	return nil, os.ErrNotExist
}

func readSecureFile(root, path string, maxBytes, remaining int64) ([]byte, int64, error) {
	if remaining <= 0 {
		return nil, 0, &sourceError{code: "total_byte_limit", message: "Total skill source byte limit reached."}
	}
	if err := verifyNoSymlinkPath(root, path); err != nil {
		return nil, 0, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !before.Mode().IsRegular() {
		return nil, 0, &sourceError{code: "not_regular_file", message: "Skill source must be a regular file."}
	}
	if before.Size() > maxBytes {
		return nil, 0, &sourceError{code: "file_byte_limit", message: fmt.Sprintf("Skill source exceeds %d bytes.", maxBytes)}
	}
	if before.Size() > remaining {
		return nil, 0, &sourceError{code: "total_byte_limit", message: "Skill source exceeds the remaining total byte budget."}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, 0, &sourceError{code: "path_changed", message: "Skill source file changed while being opened."}
	}
	limit := maxBytes
	if remaining < limit {
		limit = remaining
	}
	content, err := io.ReadAll(io.LimitReader(file, limit))
	readBytes := int64(len(content))
	if err != nil {
		return nil, readBytes, err
	}
	final, err := file.Stat()
	if err != nil || !os.SameFile(before, final) || final.Size() != before.Size() || !final.ModTime().Equal(before.ModTime()) {
		return nil, readBytes, &sourceError{code: "path_changed", message: "Skill source file changed while being read."}
	}
	if final.Size() > limit {
		return nil, readBytes, &sourceError{code: "file_byte_limit", message: "Skill source grew beyond its byte limit while being read."}
	}
	return content, readBytes, nil
}

func verifyNoSymlinkPath(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return &sourceError{code: "path_escape", message: "Skill source path escapes its configured root."}
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return &sourceError{code: "symlink_rejected", message: "Symlinked skill source path was rejected."}
		}
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	if !sameFilesystemPath(filepath.Clean(target), filepath.Clean(resolved)) {
		return &sourceError{code: "symlink_rejected", message: "Skill source path resolves through a symlink or junction."}
	}
	return nil
}

func sameFilesystemPath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func pathDepth(relative string) int {
	clean := strings.Trim(filepath.ToSlash(relative), "/")
	if clean == "" {
		return 0
	}
	return len(strings.Split(clean, "/"))
}

func joinRelative(parts ...string) string {
	joined := filepath.Join(parts...)
	return filepath.ToSlash(joined)
}

func diagnostic(adapter Adapter, relativePath, code string, severity Severity, message string) Diagnostic {
	return Diagnostic{
		Path:        filepath.ToSlash(relativePath),
		Adapter:     adapter.ID,
		AdapterRank: adapter.Rank,
		Code:        code,
		Severity:    severity,
		Message:     boundedDiagnosticMessage(message),
	}
}

func boundedDiagnosticMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) <= MaxDiagnosticMessageBytes {
		return message
	}
	var builder strings.Builder
	for _, char := range message {
		if builder.Len()+len(string(char)) > MaxDiagnosticMessageBytes-3 {
			break
		}
		builder.WriteRune(char)
	}
	builder.WriteString("...")
	return builder.String()
}

func rootProvenanceID(root string) string {
	canonical := filepath.Clean(root)
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	sum := sha256.Sum256([]byte("skillsource-root\x00" + canonical))
	return hex.EncodeToString(sum[:])
}

func safeRelativeLess(left, right string) bool {
	leftFolded, rightFolded := strings.ToLower(filepath.ToSlash(left)), strings.ToLower(filepath.ToSlash(right))
	if leftFolded != rightFolded {
		return leftFolded < rightFolded
	}
	return filepath.ToSlash(left) < filepath.ToSlash(right)
}

func markCommandConflicts(result *Result, rootID string) {
	type conflictKey struct {
		rank    int
		command string
	}
	groups := make(map[conflictKey][]int)
	for index := range result.Candidates {
		candidate := &result.Candidates[index]
		key := conflictKey{rank: candidate.AdapterRank, command: strings.ToLower(candidate.Skill.Command)}
		groups[key] = append(groups[key], index)
	}
	for key, indexes := range groups {
		if len(indexes) < 2 {
			continue
		}
		paths := make([]string, 0, len(indexes))
		for _, index := range indexes {
			candidate := &result.Candidates[index]
			candidate.ConflictStatus = ConflictCommand
			item := diagnostic(candidate.Adapter, candidate.RelativePath, "command_conflict", SeverityError, "Multiple skills at the same adapter rank define the same command.")
			candidate.Diagnostics = append(candidate.Diagnostics, item)
			result.Diagnostics = append(result.Diagnostics, item)
			paths = append(paths, candidate.RelativePath)
		}
		sort.Slice(paths, func(i, j int) bool { return safeRelativeLess(paths[i], paths[j]) })
		result.Conflicts = append(result.Conflicts, Conflict{Scope: rootID, Command: key.command, AdapterRank: key.rank, CandidatePaths: paths})
	}
}

func sortResult(result *Result) {
	sort.SliceStable(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].AdapterRank != result.Candidates[j].AdapterRank {
			return result.Candidates[i].AdapterRank < result.Candidates[j].AdapterRank
		}
		return safeRelativeLess(result.Candidates[i].RelativePath, result.Candidates[j].RelativePath)
	})
	sort.SliceStable(result.Conflicts, func(i, j int) bool {
		if result.Conflicts[i].AdapterRank != result.Conflicts[j].AdapterRank {
			return result.Conflicts[i].AdapterRank < result.Conflicts[j].AdapterRank
		}
		return result.Conflicts[i].Command < result.Conflicts[j].Command
	})
	sort.SliceStable(result.Diagnostics, func(i, j int) bool {
		left, right := result.Diagnostics[i], result.Diagnostics[j]
		if left.AdapterRank != right.AdapterRank {
			return left.AdapterRank < right.AdapterRank
		}
		if left.Path != right.Path {
			return safeRelativeLess(left.Path, right.Path)
		}
		if left.Adapter != right.Adapter {
			return left.Adapter < right.Adapter
		}
		return left.Code < right.Code
	})
}

type sourceError struct {
	code    string
	message string
}

func (err *sourceError) Error() string { return err.message }

func errorCode(err error) string {
	var typed *sourceError
	if errors.As(err, &typed) {
		return typed.code
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}
	return "source_io_error"
}

func safeErrorMessage(err error) string {
	var typed *sourceError
	if errors.As(err, &typed) {
		return typed.message
	}
	if errors.Is(err, os.ErrPermission) {
		return "Permission denied while reading skill source path."
	}
	return "Unable to read skill source path."
}

func diagnosticsContainCode(diagnostics []Diagnostic, code string) bool {
	for _, item := range diagnostics {
		if item.Code == code {
			return true
		}
	}
	return false
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
