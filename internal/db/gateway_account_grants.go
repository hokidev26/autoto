package db

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const gatewayAccountGrantIDMaxBytes = 128

type GatewayAccountGrant struct {
	Provider  string `json:"provider"`
	AccountID string `json:"accountId"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func (s *gatewayStore) ListGatewayAccountGrants(ctx context.Context, provider string) ([]GatewayAccountGrant, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("database store is unavailable")
	}
	provider, err := validateGatewayAccountGrantID(provider)
	if err != nil {
		return nil, errors.New("invalid gateway account grant provider")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT provider, account_id, created_at, updated_at FROM gateway_account_grants WHERE provider = ? ORDER BY account_id ASC`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	grants := make([]GatewayAccountGrant, 0)
	for rows.Next() {
		var grant GatewayAccountGrant
		if err := rows.Scan(&grant.Provider, &grant.AccountID, &grant.CreatedAt, &grant.UpdatedAt); err != nil {
			return nil, err
		}
		storedProvider, err := validateGatewayAccountGrantID(grant.Provider)
		if err != nil || storedProvider != grant.Provider {
			return nil, errors.New("invalid stored gateway account grant")
		}
		storedAccountID, err := validateGatewayAccountGrantID(grant.AccountID)
		if err != nil || storedAccountID != grant.AccountID || grant.CreatedAt == "" || grant.UpdatedAt == "" {
			return nil, errors.New("invalid stored gateway account grant")
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

func (s *gatewayStore) ListSharedGatewayAccountIDs(ctx context.Context, provider string) ([]string, error) {
	grants, err := s.ListGatewayAccountGrants(ctx, provider)
	if err != nil {
		return nil, err
	}
	accountIDs := make([]string, 0, len(grants))
	for _, grant := range grants {
		accountIDs = append(accountIDs, grant.AccountID)
	}
	return accountIDs, nil
}

func (s *gatewayStore) SetGatewayAccountGrant(ctx context.Context, provider, accountID string, shared bool) error {
	if !shared {
		return s.DeleteGatewayAccountGrant(ctx, provider, accountID)
	}
	if s == nil || s.db == nil {
		return errors.New("database store is unavailable")
	}
	provider, accountID, err := validateGatewayAccountGrantKey(provider, accountID)
	if err != nil {
		return err
	}
	now := Now()
	_, err = s.db.ExecContext(ctx, `
INSERT INTO gateway_account_grants (provider, account_id, created_at, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(provider, account_id) DO UPDATE SET updated_at = excluded.updated_at
`, provider, accountID, now, now)
	return err
}

func (s *gatewayStore) DeleteGatewayAccountGrant(ctx context.Context, provider, accountID string) error {
	if s == nil || s.db == nil {
		return errors.New("database store is unavailable")
	}
	provider, accountID, err := validateGatewayAccountGrantKey(provider, accountID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM gateway_account_grants WHERE provider = ? AND account_id = ?`, provider, accountID)
	return err
}

func validateGatewayAccountGrantKey(provider, accountID string) (string, string, error) {
	provider, err := validateGatewayAccountGrantID(provider)
	if err != nil {
		return "", "", errors.New("invalid gateway account grant provider")
	}
	accountID, err = validateGatewayAccountGrantID(accountID)
	if err != nil {
		return "", "", errors.New("invalid gateway account grant account id")
	}
	return provider, accountID, nil
}

func validateGatewayAccountGrantID(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("invalid utf-8")
	}
	for _, char := range value {
		if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return "", errors.New("control characters are not allowed")
		}
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > gatewayAccountGrantIDMaxBytes {
		return "", errors.New("invalid length")
	}
	return value, nil
}
