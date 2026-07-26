package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// MultiEditTool applies several replacements to one file as a single unit.
//
// Doing the same work with repeated Edit calls has two real problems. Each call
// is a separate approval and a separate round trip, so a ten-hunk change costs
// ten of both. Worse, a failure halfway through leaves the file in a state that
// is neither the old version nor the intended new one, and the model has to
// work out which hunks already landed. Here every hunk is validated and applied
// in memory first; the file is written once, or not at all.
type MultiEditTool struct{}

const multiEditMaxHunks = 100

type multiEditHunk struct {
	OldString  string `json:"old_string" desc:"Exact text to replace, including indentation. Must appear exactly once unless replace_all is set."`
	NewString  string `json:"new_string" desc:"Replacement text. Must differ from old_string."`
	ReplaceAll bool   `json:"replace_all,omitempty" desc:"Replace every occurrence of this hunk's old_string instead of requiring it to be unique."`
}

type multiEditInput struct {
	FilePath string          `json:"file_path" desc:"Path to the existing file to modify, absolute or relative to the working directory."`
	Edits    []multiEditHunk `json:"edits" desc:"Replacements applied in order, each seeing the result of the previous one. All are applied together or none are."`
}

func (MultiEditTool) Name() string { return "MultiEdit" }
func (MultiEditTool) Description() string {
	return "Apply several ordered text replacements to one file atomically. Either every edit applies or the file is left unchanged."
}
func (MultiEditTool) Schema() any               { return multiEditInput{} }
func (MultiEditTool) Risk(json.RawMessage) Risk { return RiskWrite }

func (MultiEditTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input multiEditInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(input.FilePath) == "" {
		return Result{Output: "file_path is required", IsError: true}, nil
	}
	if len(input.Edits) == 0 {
		return Result{Output: "edits must contain at least one replacement", IsError: true}, nil
	}
	if len(input.Edits) > multiEditMaxHunks {
		return Result{Output: fmt.Sprintf("edits exceeds the %d replacement maximum", multiEditMaxHunks), IsError: true}, nil
	}
	path, err := resolveInCWD(env.CWD, input.FilePath)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	original := string(data)
	updated := original
	replacements := 0
	// Apply every hunk in memory. Any failure returns before the write, so the
	// file on disk never reflects a partial application.
	for index, hunk := range input.Edits {
		if hunk.OldString == "" {
			return Result{Output: fmt.Sprintf("edits[%d]: old_string is required", index), IsError: true}, nil
		}
		if hunk.OldString == hunk.NewString {
			return Result{Output: fmt.Sprintf("edits[%d]: old_string and new_string must differ", index), IsError: true}, nil
		}
		count := strings.Count(updated, hunk.OldString)
		if count == 0 {
			// Report the ordinal, because a later hunk can legitimately depend on
			// an earlier one having already rewritten the surrounding text.
			return Result{Output: fmt.Sprintf("edits[%d]: old_string not found", index), IsError: true}, nil
		}
		if !hunk.ReplaceAll && count != 1 {
			return Result{Output: fmt.Sprintf("edits[%d]: old_string is not unique; found %d occurrences", index, count), IsError: true}, nil
		}
		limit := 1
		applied := 1
		if hunk.ReplaceAll {
			limit = -1
			applied = count
		}
		updated = strings.Replace(updated, hunk.OldString, hunk.NewString, limit)
		replacements += applied
	}
	if updated == original {
		return Result{Output: "edits produced no change", IsError: true}, nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}

	diff, diffTruncated := buildEditDiff(original, updated, editDiffDisplayPath(env.CWD, input.FilePath, path))
	meta := map[string]any{"path": path, "replacements": replacements, "edits": len(input.Edits), "diff": diff}
	if diffTruncated {
		meta["diffTruncated"] = true
	}
	return Result{
		Output: fmt.Sprintf("Edited %s (%d edit(s), %d replacement(s))", path, len(input.Edits), replacements),
		Meta:   meta,
	}, nil
}
