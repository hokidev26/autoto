package agent

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"autoto/internal/workspacefs"
)

const (
	maxProjectInstructionReadBytes = 64 * 1024
	maxProjectInstructionFileRunes = 12_000
)

var projectInstructionFilenames = []string{"AGENTS.md", "CLAUDE.md"}

type projectInstructionBundle struct {
	Text  string
	Files []projectInstructionFile
}

type projectInstructionFile struct {
	Name      string
	Path      string
	Truncated bool
}

func loadProjectInstructions(cwd string) projectInstructionBundle {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return projectInstructionBundle{}
	}
	workspace, err := workspacefs.New(cwd)
	if err != nil {
		return projectInstructionBundle{}
	}
	sections := make([]string, 0, len(projectInstructionFilenames))
	files := make([]projectInstructionFile, 0, len(projectInstructionFilenames))
	for _, name := range projectInstructionFilenames {
		file, readErr := workspace.ReadFile(name)
		if readErr != nil {
			continue
		}
		content, truncated := normalizeProjectInstructionContent(file.Content, file.Truncated)
		if strings.TrimSpace(content) == "" {
			continue
		}
		// Path is intentionally workspace-relative. Event consumers must never
		// receive an absolute host path for project instruction sources.
		files = append(files, projectInstructionFile{Name: name, Path: file.Path, Truncated: truncated})
		truncationNote := ""
		if truncated {
			truncationNote = "\n\n[Autoto note: this instruction file was truncated to fit the safety limit.]"
		}
		sections = append(sections, fmt.Sprintf("### %s\n\n%s%s", name, strings.TrimSpace(content), truncationNote))
	}
	if len(sections) == 0 {
		return projectInstructionBundle{}
	}
	return projectInstructionBundle{Text: "## Project instructions loaded by Autoto\n\n" + strings.Join(sections, "\n\n"), Files: files}
}

func normalizeProjectInstructionContent(content string, truncated bool) (string, bool) {
	data := []byte(content)
	if len(data) > maxProjectInstructionReadBytes {
		data = data[:maxProjectInstructionReadBytes]
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
		truncated = true
	}
	text := string(data)
	trimmedRunes := []rune(strings.TrimSpace(text))
	if len(trimmedRunes) > maxProjectInstructionFileRunes {
		truncated = true
	}
	return truncateRunes(text, maxProjectInstructionFileRunes), truncated
}

func (b projectInstructionBundle) eventData() map[string]any {
	if len(b.Files) == 0 {
		return nil
	}
	files := make([]map[string]any, 0, len(b.Files))
	for _, file := range b.Files {
		files = append(files, map[string]any{"name": file.Name, "path": file.Path, "truncated": file.Truncated})
	}
	return map[string]any{"files": files}
}
