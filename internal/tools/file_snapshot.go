package tools

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

const (
	contentHashHexLen       = 16
	maxFileOutlineEntries   = 80
	maxFileOutlineLineRunes = 120
)

type fileFingerprint struct {
	hash  string
	lines int
}

func hashBytes(data []byte) fileFingerprint {
	sum := sha256.Sum256(data)
	return fileFingerprint{hash: hex.EncodeToString(sum[:])[:contentHashHexLen], lines: countFileLines(data)}
}

func hashFileOnDisk(path string) (fileFingerprint, error) {
	file, err := os.Open(path)
	if err != nil {
		return fileFingerprint{}, err
	}
	defer file.Close()
	hasher := sha256.New()
	var lines int
	var size int64
	lastWasNL := false
	buf := make([]byte, 32*1024)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			_, _ = hasher.Write(buf[:n])
			size += int64(n)
			lines += bytes.Count(buf[:n], []byte{'\n'})
			lastWasNL = buf[n-1] == '\n'
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fileFingerprint{}, readErr
		}
	}
	if size > 0 && !lastWasNL {
		lines++
	}
	sum := hasher.Sum(nil)
	return fileFingerprint{hash: hex.EncodeToString(sum)[:contentHashHexLen], lines: lines}, nil
}

func countFileLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func formatFileSnapshotHeader(displayPath, hash string, lines int) string {
	displayPath = strings.TrimSpace(displayPath)
	if displayPath == "" {
		displayPath = "file"
	}
	return fmt.Sprintf("file %s hash=%s lines=%d", displayPath, hash, lines)
}

func withFileSnapshot(displayPath string, fp fileFingerprint, body string, meta map[string]any) Result {
	output := formatFileSnapshotHeader(displayPath, fp.hash, fp.lines)
	if body != "" {
		output += "\n" + body
	}
	return Result{Output: output, Meta: attachFileSnapshotMeta(meta, fp)}
}

func attachFileSnapshotMeta(meta map[string]any, fp fileFingerprint) map[string]any {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["contentHash"] = fp.hash
	meta["lineCount"] = fp.lines
	return meta
}

func expectedHashMatches(expected, live string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	live = strings.ToLower(strings.TrimSpace(live))
	if expected == "" {
		return true
	}
	if !isHex(expected) || live == "" {
		return false
	}
	if expected == live {
		return true
	}
	if len(expected) == 64 && strings.HasPrefix(expected, live) {
		return true
	}
	return false
}

func staleFileHashError(fp fileFingerprint) string {
	return fmt.Sprintf("File has changed since the last Read (hash=%s lines=%d). Re-read the file before editing.", fp.hash, fp.lines)
}

func isHex(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func numberTextLines(text string, startLine int) string {
	if text == "" {
		return ""
	}
	if startLine < 1 {
		startLine = 1
	}
	keepNL := strings.HasSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	if keepNL {
		lines = lines[:len(lines)-1]
	}
	var builder strings.Builder
	for i, line := range lines {
		if i > 0 {
			builder.WriteByte('\n')
		}
		fmt.Fprintf(&builder, "%6d|%s", startLine+i, line)
	}
	if keepNL {
		builder.WriteByte('\n')
	}
	return builder.String()
}

func formatNumberedLine(lineNumber int, text string) string {
	return fmt.Sprintf("%6d|%s", lineNumber, text)
}

func scanFileOutline(path string, maxEntries int) []string {
	if maxEntries <= 0 {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxReadLineBytes)
	entries := make([]string, 0, min(maxEntries, 16))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if !isCheapOutlineLine(line) {
			continue
		}
		entries = append(entries, formatNumberedLine(lineNumber, clipOutlineLine(line)))
		if len(entries) >= maxEntries {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil
	}
	return entries
}

func formatFileOutline(entries []string, capped bool) string {
	if len(entries) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("outline:")
	for _, entry := range entries {
		builder.WriteByte('\n')
		builder.WriteString(entry)
	}
	if capped {
		builder.WriteString("\n...[outline truncated]")
	}
	return builder.String()
}

func clipOutlineLine(line string) string {
	runes := []rune(strings.TrimRightFunc(line, unicode.IsSpace))
	if len(runes) <= maxFileOutlineLineRunes {
		return string(runes)
	}
	return string(runes[:maxFileOutlineLineRunes]) + "…"
}

func isCheapOutlineLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if isMarkdownHeading(trimmed) {
		return true
	}
	for _, prefix := range []string{"export ", "pub ", "public ", "private ", "protected ", "static ", "async "} {
		if after, ok := strings.CutPrefix(trimmed, prefix); ok {
			trimmed = strings.TrimSpace(after)
		}
	}
	switch {
	case strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "func(") || strings.HasPrefix(trimmed, "func\t"):
		return true
	case strings.HasPrefix(trimmed, "class ") || strings.HasPrefix(trimmed, "class\t"):
		return true
	case strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "def\t"):
		return true
	case strings.HasPrefix(trimmed, "type ") || strings.HasPrefix(trimmed, "type\t"):
		return true
	case strings.HasPrefix(trimmed, "fn ") || strings.HasPrefix(trimmed, "fn\t"):
		return true
	default:
		return false
	}
}

func isMarkdownHeading(line string) bool {
	if line == "" || line[0] != '#' {
		return false
	}
	n := 0
	for n < len(line) && n < 6 && line[n] == '#' {
		n++
	}
	if n == 0 || n >= len(line) {
		return false
	}
	return line[n] == ' ' || line[n] == '\t'
}
