package db

import (
	"context"
	"strings"
	"unicode/utf8"
)

const (
	messageAuthorDisplayNameRunes = 80
	messageAuthorInitialsRunes    = 4
	messageAuthorHandleRunes      = 64
)

// ListMessageAuthors returns public transcript identities for the given user
// ids. Missing ids are omitted. Avatar images, emails, and other profile
// fields are never included.
func (s *userStore) ListMessageAuthors(ctx context.Context, userIDs []string) (map[string]MessageAuthor, error) {
	ids := uniqueMessageAuthorIDs(userIDs)
	if len(ids) == 0 {
		return map[string]MessageAuthor{}, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, AccountPreferenceScopeUser)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.reader().QueryContext(ctx, `
SELECT u.id, u.handle,
  COALESCE(json_extract(p.profile_json, '$.displayName'), ''),
  COALESCE(json_extract(p.profile_json, '$.workspaceLabel'), ''),
  COALESCE(json_extract(p.profile_json, '$.avatarInitials'), '')
FROM users u
LEFT JOIN account_preferences p
  ON p.scope_kind = ? AND p.scope_id = u.id
WHERE u.id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	authors := make(map[string]MessageAuthor, len(ids))
	for rows.Next() {
		var id, handle, displayName, workspaceLabel, avatarInitials string
		if err := rows.Scan(&id, &handle, &displayName, &workspaceLabel, &avatarInitials); err != nil {
			return nil, err
		}
		authors[id] = publicMessageAuthor(id, handle, displayName, workspaceLabel, avatarInitials)
	}
	return authors, rows.Err()
}

func uniqueMessageAuthorIDs(userIDs []string) []string {
	seen := make(map[string]struct{}, len(userIDs))
	ids := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		id = strings.TrimSpace(id)
		if id == "" || id == "api" || strings.HasPrefix(id, "peer:") {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) >= MaxMessagePageLimit {
			break
		}
	}
	return ids
}

func publicMessageAuthor(id, handle, displayName, workspaceLabel, avatarInitials string) MessageAuthor {
	handle = clipMessageAuthorRunes(handle, messageAuthorHandleRunes)
	displayName = clipMessageAuthorRunes(displayName, messageAuthorDisplayNameRunes)
	if displayName == "" {
		displayName = clipMessageAuthorRunes(workspaceLabel, messageAuthorDisplayNameRunes)
	}
	if displayName == "" {
		displayName = handle
	}
	initials := strings.ToUpper(clipMessageAuthorRunes(avatarInitials, messageAuthorInitialsRunes))
	if initials == "" {
		initials = initialsFromHandle(handle)
	}
	return MessageAuthor{
		ID:             strings.TrimSpace(id),
		Handle:         handle,
		DisplayName:    displayName,
		AvatarInitials: initials,
	}
}

func initialsFromHandle(handle string) string {
	runes := []rune(strings.TrimSpace(handle))
	switch len(runes) {
	case 0:
		return "AT"
	case 1:
		return strings.ToUpper(string(runes[0]))
	default:
		return strings.ToUpper(string(runes[:2]))
	}
}

func clipMessageAuthorRunes(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if maximum <= 0 || utf8.RuneCountInString(value) <= maximum {
		return value
	}
	return string([]rune(value)[:maximum])
}
