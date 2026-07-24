package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

func NewSnapshot(hooks []Hook, now time.Time) (Snapshot, error) {
	copied := make([]Hook, 0, len(hooks))
	for _, hook := range hooks {
		canonical, err := NormalizeAndValidateHook(hook)
		if err != nil {
			return Snapshot{}, err
		}
		copied = append(copied, cloneHook(canonical))
	}
	sort.SliceStable(copied, func(i, j int) bool {
		if copied[i].Priority != copied[j].Priority {
			return copied[i].Priority > copied[j].Priority
		}
		return copied[i].ID < copied[j].ID
	})
	payload, err := json.Marshal(copied)
	if err != nil {
		return Snapshot{}, err
	}
	sum := sha256.Sum256(payload)
	return Snapshot{Version: 1, Digest: hex.EncodeToString(sum[:]), CreatedAt: now.UTC().Format(time.RFC3339Nano), Hooks: copied}, nil
}

func (snapshot Snapshot) Match(event Event) []Hook {
	result := make([]Hook, 0)
	for _, hook := range snapshot.Hooks {
		if Matches(hook, event) {
			result = append(result, cloneHook(hook))
		}
	}
	return result
}

func cloneHook(hook Hook) Hook {
	data, _ := json.Marshal(hook)
	var cloned Hook
	_ = json.Unmarshal(data, &cloned)
	return cloned
}
