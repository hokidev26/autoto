package skills

import (
	"strings"
	"testing"
)

func TestParseAgentSkillStrictFrontmatterAndNormalize(t *testing.T) {
	parsed, err := ParseAgentSkillForDirectory(`---
name: review-diff
description: >-
  Review a proposed change without executing it.
license: Apache-2.0
compatibility: Requires a Git worktree.
metadata:
  author: Code Harbor
  version: "1"
allowed-tools: Read Grep
---
# Review Diff

Review the supplied diff and report concrete risks.
`, "review-diff")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Name != "review-diff" || parsed.Description != "Review a proposed change without executing it." || parsed.Metadata["author"] != "Code Harbor" {
		t.Fatalf("unexpected parsed Agent Skill: %+v", parsed)
	}
	if len(parsed.Diagnostics) != 1 || parsed.Diagnostics[0].Code != "allowed_tools_ignored" {
		t.Fatalf("expected ignored permission diagnostic, got %+v", parsed.Diagnostics)
	}
	normalized, err := NormalizeAgentSkill(parsed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Command != "/review-diff" || normalized.Prompt != "# Review Diff\n\nReview the supplied diff and report concrete risks." {
		t.Fatalf("unexpected normalized skill: %+v", normalized)
	}
}

func TestParseAgentSkillRejectsNonCanonicalOrAmbiguousFrontmatter(t *testing.T) {
	tests := map[string]string{
		"missing frontmatter": "# Skill\nPrompt",
		"missing name":        "---\ndescription: useful\n---\nprompt",
		"invalid name":        "---\nname: Review_Diff\ndescription: useful\n---\nprompt",
		"duplicate":           "---\nname: one\nname: two\ndescription: useful\n---\nprompt",
		"unknown":             "---\nname: one\ndescription: useful\ncommand: /one\n---\nprompt",
		"non string":          "---\nname: one\ndescription: true\n---\nprompt",
		"metadata non string": "---\nname: one\ndescription: useful\nmetadata:\n  version: 1\n---\nprompt",
		"anchor":              "---\nname: one\ndescription: &desc useful\n---\nprompt",
		"empty body":          "---\nname: one\ndescription: useful\n---\n  \n",
		"extra yaml document": "---\nname: one\ndescription: useful\n...\nname: two\n---\nprompt",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAgentSkill(content); err == nil {
				t.Fatalf("expected rejection for %s", name)
			}
		})
	}
}

func TestParseAgentSkillRequiresDirectoryNameMatch(t *testing.T) {
	content := "---\nname: expected-name\ndescription: useful\n---\nprompt"
	if _, err := ParseAgentSkillForDirectory(content, "other-name"); err == nil {
		t.Fatal("expected directory/name mismatch rejection")
	}
}

func TestNormalizeAgentSkillKeepsPromptAndCommandIndependentFromSidecar(t *testing.T) {
	document, err := ParseAgentSkill("---\nname: safe-command\ndescription: base description\n---\nAuthoritative body")
	if err != nil {
		t.Fatal(err)
	}
	sidecar := OpenAISidecar{DisplayName: "Friendly Display", ShortDescription: "Short display summary"}
	normalized, err := NormalizeAgentSkill(document, &sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Name != "Friendly Display" || normalized.Description != "Short display summary" {
		t.Fatalf("sidecar display metadata not applied: %+v", normalized)
	}
	if normalized.Command != "/safe-command" || normalized.Prompt != "Authoritative body" {
		t.Fatalf("sidecar changed semantic fields: %+v", normalized)
	}
}

func TestParseAgentSkillHonorsExistingNormalizeLimits(t *testing.T) {
	description := strings.Repeat("d", MaxDescriptionBytes+1)
	document, err := ParseAgentSkill("---\nname: valid-name\ndescription: " + description + "\n---\nprompt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeAgentSkill(document, nil); err == nil {
		t.Fatal("expected existing Normalize description limit to remain final")
	}
}
