package db

import (
	"context"
	"strings"
	"unicode/utf8"
)

const (
	maxConversationSearchAgents       = 300
	maxConversationSearchRows         = 40
	maxConversationSearchSnippetRunes = 160
	conversationSearchWindowRunes     = 32
)

// AgentMessageSearchHit is one transcript match. Snippet is a short window
// around the query, not the full message.
type AgentMessageSearchHit struct {
	MessageID string
	AgentID   string
	Role      string
	CreatedAt string
	Snippet   string
}

// SearchAgentMessages finds current user/assistant turns whose text contains
// query, limited to the supplied agent IDs. The caller must already have
// filtered those IDs to agents the request may see.
func (s *messageStore) SearchAgentMessages(ctx context.Context, agentIDs []string, query string, limit int) ([]AgentMessageSearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(agentIDs) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > maxConversationSearchRows {
		limit = maxConversationSearchRows
	}
	if len(agentIDs) > maxConversationSearchAgents {
		agentIDs = agentIDs[:maxConversationSearchAgents]
	}
	args := make([]any, 0, len(agentIDs)+2)
	for _, id := range agentIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		args = append(args, id)
	}
	if len(args) == 0 {
		return nil, nil
	}
	args = append(args, strings.ToLower(query), limit)
	rows, err := s.reader().QueryContext(ctx, `
SELECT id, agent_id, role, COALESCE(content_text,''), created_at
FROM agent_messages
WHERE superseded_at IS NULL
  AND COALESCE(parent_tool_use_id, '') = ''
  AND role IN ('user', 'assistant')
  AND agent_id IN (`+placeholders(len(args)-2)+`)
  AND instr(lower(COALESCE(content_text,'')), ?) > 0
ORDER BY created_at DESC, id DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := make([]AgentMessageSearchHit, 0)
	for rows.Next() {
		var hit AgentMessageSearchHit
		var content string
		if err := rows.Scan(&hit.MessageID, &hit.AgentID, &hit.Role, &content, &hit.CreatedAt); err != nil {
			return nil, err
		}
		hit.Snippet = snippetAround(content, query, maxConversationSearchSnippetRunes)
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}

func snippetAround(text, query string, maxRunes int) string {
	if maxRunes < 24 {
		maxRunes = 24
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	lowerText := strings.ToLower(text)
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	byteIndex := strings.Index(lowerText, lowerQuery)
	if byteIndex < 0 {
		return clipRunes(text, maxRunes)
	}
	runeIndex := utf8.RuneCountInString(text[:byteIndex])
	start := runeIndex - conversationSearchWindowRunes
	if start < 0 {
		start = 0
	}
	end := start + maxRunes
	if end > len(runes) {
		end = len(runes)
	}
	snippet := string(runes[start:end])
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(runes) {
		snippet += "…"
	}
	return snippet
}

func clipRunes(text string, maxRunes int) string {
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "…"
}
