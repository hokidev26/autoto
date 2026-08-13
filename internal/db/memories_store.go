package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

func (s *memoryStore) CreateMemory(ctx context.Context, memory Memory) (Memory, error) {
	canonical, keywordsJSON, err := canonicalMemory(memory, false)
	if err != nil {
		return Memory{}, err
	}
	if canonical.ID == "" {
		canonical.ID = NewID()
	}
	if err := validateMemoryID(canonical.ID); err != nil {
		return Memory{}, err
	}
	now := Now()
	canonical.CreatedAt = now
	canonical.UpdatedAt = now
	_, err = s.db.ExecContext(ctx, `INSERT INTO memories (id, agent_id, content, keywords_json, pinned, archived_at, created_at, updated_at) VALUES (?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?, ?)`, canonical.ID, canonical.AgentID, canonical.Content, keywordsJSON, boolInt(canonical.Pinned), canonical.ArchivedAt, canonical.CreatedAt, canonical.UpdatedAt)
	if err != nil {
		if isUniqueConstraint(err) {
			return Memory{}, fmt.Errorf("%w: memory id already exists", ErrConflict)
		}
		if isForeignKeyConstraint(err) {
			return Memory{}, fmt.Errorf("%w: memory agent does not exist", ErrConflict)
		}
		return Memory{}, err
	}
	return canonical, nil
}

func (s *memoryStore) GetMemory(ctx context.Context, id string) (Memory, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Memory{}, sql.ErrNoRows
	}
	return scanMemory(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT id, COALESCE(agent_id,''), content, keywords_json, pinned, COALESCE(archived_at,''), created_at, updated_at FROM memories WHERE id = ?`, id).Scan(dest...)
	})
}

// ListMemories accepts no options, a MemoryListOptions value, a query string,
// or a query string followed by includeArchived. Results are pinned first and
// then newest-updated first.
func (s *memoryStore) ListMemories(ctx context.Context, args ...any) ([]Memory, error) {
	options, err := parseMemoryListOptions(args)
	if err != nil {
		return nil, err
	}
	scope, agentID, err := canonicalMemoryScope(options.Scope, options.AgentID)
	if err != nil {
		return nil, err
	}
	// A scoped listing must not leak another conversation's memories, so the
	// filter belongs in SQL rather than in the caller.
	scopeClause := ""
	scopeArgs := []any{}
	switch scope {
	case MemoryScopeGlobal:
		scopeClause = ` AND agent_id IS NULL`
	case MemoryScopeAgent:
		scopeClause = ` AND agent_id = ?`
		scopeArgs = append(scopeArgs, agentID)
	}
	queryArgs := append([]any{boolInt(options.IncludeArchived)}, scopeArgs...)
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(agent_id,''), content, keywords_json, pinned, COALESCE(archived_at,''), created_at, updated_at FROM memories WHERE (? = 1 OR archived_at IS NULL)`+scopeClause+` ORDER BY pinned DESC, updated_at DESC, id ASC`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	query := strings.ToLower(strings.TrimSpace(options.Query))
	memories := make([]Memory, 0)
	for rows.Next() {
		memory, err := scanMemory(rows.Scan)
		if err != nil {
			return nil, err
		}
		if query == "" || memoryMatchesQuery(memory, query) {
			memories = append(memories, memory)
		}
	}
	return memories, rows.Err()
}

func (s *memoryStore) UpdateMemory(ctx context.Context, memory Memory) (Memory, error) {
	canonical, keywordsJSON, err := canonicalMemory(memory, true)
	if err != nil {
		return Memory{}, err
	}
	existing, err := s.GetMemory(ctx, canonical.ID)
	if err != nil {
		return Memory{}, err
	}
	canonical.CreatedAt = existing.CreatedAt
	canonical.UpdatedAt = nextMemoryUpdatedAt(existing.UpdatedAt)
	// Ownership is immutable: moving a memory between conversations would silently
	// change who can see it, so an update always keeps the stored agent.
	canonical.AgentID = existing.AgentID
	result, err := s.db.ExecContext(ctx, `UPDATE memories SET content = ?, keywords_json = ?, pinned = ?, archived_at = NULLIF(?, ''), updated_at = ? WHERE id = ?`, canonical.Content, keywordsJSON, boolInt(canonical.Pinned), canonical.ArchivedAt, canonical.UpdatedAt, canonical.ID)
	if err != nil {
		return Memory{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Memory{}, err
	} else if affected != 1 {
		return Memory{}, sql.ErrNoRows
	}
	return canonical, nil
}

func (s *memoryStore) DeleteMemory(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return sql.ErrNoRows
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *memoryStore) SetMemoryPinned(ctx context.Context, id string, pinned bool) (Memory, error) {
	memory, err := s.GetMemory(ctx, id)
	if err != nil {
		return Memory{}, err
	}
	memory.Pinned = pinned
	return s.UpdateMemory(ctx, memory)
}

func (s *memoryStore) PinMemory(ctx context.Context, id string, pinned ...bool) (Memory, error) {
	value := true
	if len(pinned) > 1 {
		return Memory{}, errors.New("pin memory accepts at most one pinned value")
	}
	if len(pinned) == 1 {
		value = pinned[0]
	}
	return s.SetMemoryPinned(ctx, id, value)
}

func (s *memoryStore) UnpinMemory(ctx context.Context, id string) (Memory, error) {
	return s.SetMemoryPinned(ctx, id, false)
}

func (s *memoryStore) SetMemoryArchived(ctx context.Context, id string, archived bool) (Memory, error) {
	memory, err := s.GetMemory(ctx, id)
	if err != nil {
		return Memory{}, err
	}
	if archived {
		memory.ArchivedAt = Now()
	} else {
		memory.ArchivedAt = ""
	}
	return s.UpdateMemory(ctx, memory)
}

func (s *memoryStore) ArchiveMemory(ctx context.Context, id string) (Memory, error) {
	return s.SetMemoryArchived(ctx, id, true)
}

func (s *memoryStore) UnarchiveMemory(ctx context.Context, id string) (Memory, error) {
	return s.SetMemoryArchived(ctx, id, false)
}

func (s *memoryStore) ListMatchingUninjectedMemories(ctx context.Context, agentID, text string, limit int) ([]Memory, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, errors.New("memory injection agent id is required")
	}
	if limit <= 0 {
		return []Memory{}, nil
	}
	// Two buckets with deliberately different rules:
	//
	//   - A memory owned by this conversation is durable state the user chose to
	//     keep. It carries no keyword requirement and ignores the injection
	//     ledger, because the system prompt is rebuilt every run: injecting it
	//     once would let the next compaction erase it for good.
	//   - A global memory is opt-in background material. It still needs a keyword
	//     hit on the trigger text and still injects at most once per agent.
	//
	// Owned memories sort first so they win the shared injection budget.
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, COALESCE(m.agent_id,''), m.content, m.keywords_json, m.pinned, COALESCE(m.archived_at,''), m.created_at, m.updated_at FROM memories m WHERE m.archived_at IS NULL AND (m.agent_id = ? OR (m.agent_id IS NULL AND m.keywords_json <> '[]' AND NOT EXISTS (SELECT 1 FROM memory_injections i WHERE i.memory_id = m.id AND i.agent_id = ?))) ORDER BY (m.agent_id IS NULL), m.pinned DESC, m.updated_at DESC, m.id ASC`, agentID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lowerText := strings.ToLower(text)
	matches := make([]Memory, 0, limit)
	for rows.Next() {
		memory, err := scanMemory(rows.Scan)
		if err != nil {
			return nil, err
		}
		if memory.AgentID == "" && (len(memory.Keywords) == 0 || !memoryKeywordsMatch(memory.Keywords, lowerText)) {
			continue
		}
		matches = append(matches, memory)
		if len(matches) == limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return matches, nil
}

// canonicalMemoryScope normalizes a listing scope and enforces that an
// agent-scoped listing actually names an agent.
func canonicalMemoryScope(scope, agentID string) (string, string, error) {
	scope = strings.ToLower(strings.TrimSpace(scope))
	agentID = strings.TrimSpace(agentID)
	switch scope {
	case "", MemoryScopeAll:
		return MemoryScopeAll, agentID, nil
	case MemoryScopeGlobal:
		return MemoryScopeGlobal, "", nil
	case MemoryScopeAgent:
		if agentID == "" {
			return "", "", errors.New("memory scope agent requires an agent id")
		}
		return MemoryScopeAgent, agentID, nil
	default:
		return "", "", fmt.Errorf("invalid memory scope %q", scope)
	}
}

func (s *memoryStore) MarkMemoriesInjected(ctx context.Context, agentID string, memoryIDs []string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("memory injection agent id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, agentID).Scan(&exists); err != nil {
		return err
	}
	ids := make([]string, 0, len(memoryIDs))
	seen := make(map[string]struct{}, len(memoryIDs))
	for _, rawID := range memoryIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return errors.New("memory injection memory id is required")
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM memories WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	injectedAt := Now()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_injections (memory_id, agent_id, injected_at) VALUES (?, ?, ?) ON CONFLICT(memory_id, agent_id) DO NOTHING`, id, agentID, injectedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func parseMemoryListOptions(args []any) (MemoryListOptions, error) {
	var options MemoryListOptions
	switch len(args) {
	case 0:
		return options, nil
	case 1:
		switch value := args[0].(type) {
		case MemoryListOptions:
			return value, nil
		case *MemoryListOptions:
			if value == nil {
				return options, nil
			}
			return *value, nil
		case string:
			options.Query = value
			return options, nil
		case bool:
			options.IncludeArchived = value
			return options, nil
		default:
			return options, errors.New("invalid memory list options")
		}
	case 2:
		query, queryOK := args[0].(string)
		includeArchived, archivedOK := args[1].(bool)
		if !queryOK || !archivedOK {
			return options, errors.New("invalid memory list options")
		}
		options.Query = query
		options.IncludeArchived = includeArchived
		return options, nil
	default:
		return options, errors.New("invalid memory list options")
	}
}

func canonicalMemory(memory Memory, requireID bool) (Memory, string, error) {
	memory.ID = strings.TrimSpace(memory.ID)
	memory.AgentID = strings.TrimSpace(memory.AgentID)
	memory.ArchivedAt = strings.TrimSpace(memory.ArchivedAt)
	if requireID || memory.ID != "" {
		if err := validateMemoryID(memory.ID); err != nil {
			return Memory{}, "", err
		}
	}
	if memory.AgentID != "" && (len(memory.AgentID) > 128 || !utf8.ValidString(memory.AgentID) || strings.ContainsRune(memory.AgentID, 0)) {
		return Memory{}, "", errors.New("invalid memory agent id")
	}
	if strings.TrimSpace(memory.Content) == "" {
		return Memory{}, "", errors.New("memory content is required")
	}
	if len(memory.Content) > MemoryContentMaxBytes {
		return Memory{}, "", fmt.Errorf("memory content exceeds %d bytes", MemoryContentMaxBytes)
	}
	if !utf8.ValidString(memory.Content) || strings.ContainsRune(memory.Content, 0) {
		return Memory{}, "", errors.New("invalid memory content")
	}
	keywords, err := normalizeMemoryKeywords(memory.Keywords)
	if err != nil {
		return Memory{}, "", err
	}
	encoded, err := json.Marshal(keywords)
	if err != nil {
		return Memory{}, "", fmt.Errorf("encode memory keywords: %w", err)
	}
	memory.Keywords = keywords
	if memory.ArchivedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, memory.ArchivedAt); err != nil {
			return Memory{}, "", errors.New("invalid memory archived_at")
		}
	}
	return memory, string(encoded), nil
}

func validateMemoryID(id string) error {
	if id == "" {
		return errors.New("memory id is required")
	}
	if len(id) > 128 || !utf8.ValidString(id) || strings.ContainsRune(id, 0) {
		return errors.New("invalid memory id")
	}
	return nil
}

func normalizeMemoryKeywords(keywords []string) ([]string, error) {
	normalized := make([]string, 0, len(keywords))
	seen := make(map[string]struct{}, len(keywords))
	for _, keyword := range keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword == "" {
			return nil, errors.New("memory keyword must not be empty")
		}
		if !utf8.ValidString(keyword) || strings.ContainsRune(keyword, 0) {
			return nil, errors.New("invalid memory keyword")
		}
		if utf8.RuneCountInString(keyword) > MemoryKeywordMaxRunes {
			return nil, fmt.Errorf("memory keyword exceeds %d runes", MemoryKeywordMaxRunes)
		}
		if _, duplicate := seen[keyword]; duplicate {
			continue
		}
		seen[keyword] = struct{}{}
		normalized = append(normalized, keyword)
		if len(normalized) > MemoryMaxKeywords {
			return nil, fmt.Errorf("memory keywords exceed maximum of %d", MemoryMaxKeywords)
		}
	}
	return normalized, nil
}

func memoryMatchesQuery(memory Memory, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(memory.Content), lowerQuery) {
		return true
	}
	return memoryKeywordsMatch(memory.Keywords, lowerQuery)
}

func memoryKeywordsMatch(keywords []string, lowerText string) bool {
	for _, keyword := range keywords {
		if strings.Contains(lowerText, keyword) {
			return true
		}
	}
	return false
}

func nextMemoryUpdatedAt(previous string) string {
	now := time.Now().UTC()
	if prior, err := time.Parse(time.RFC3339Nano, previous); err == nil && !now.After(prior) {
		now = prior.Add(time.Nanosecond)
	}
	return now.Format(time.RFC3339Nano)
}

type memoryScanner func(dest ...any) error

func scanMemory(scan memoryScanner) (Memory, error) {
	var memory Memory
	var keywordsJSON string
	var pinned int
	if err := scan(&memory.ID, &memory.AgentID, &memory.Content, &keywordsJSON, &pinned, &memory.ArchivedAt, &memory.CreatedAt, &memory.UpdatedAt); err != nil {
		return Memory{}, err
	}
	var keywords []string
	if err := json.Unmarshal([]byte(keywordsJSON), &keywords); err != nil || keywords == nil {
		return Memory{}, fmt.Errorf("stored memory keywords for %s are invalid", memory.ID)
	}
	normalized, err := normalizeMemoryKeywords(keywords)
	if err != nil {
		return Memory{}, fmt.Errorf("stored memory keywords for %s are invalid: %w", memory.ID, err)
	}
	memory.Keywords = normalized
	memory.Pinned = pinned != 0
	return memory, nil
}
