package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	maxReadBytes     = 100000
	maxReadLines     = 4000
	maxReadLineBytes = 1 << 20
	// Base64 inflates the payload by 4/3, so this cap keeps the blob Read places
	// in Result.Meta under ~5.3 MiB. It also matches the 4 MiB model-image budget
	// enforced in internal/media, so Read cannot hand back an image that the
	// provider path would reject later anyway.
	maxReadImageBytes int64 = 4 << 20
)

type ReadTool struct{}

type readInput struct {
	FilePath string `json:"file_path" desc:"Path to the file, absolute or relative to the working directory. Paths outside the working directory and sensitive files such as .env are rejected."`
	Limit    int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=100000" desc:"Maximum bytes to return. Defaults to 100000."`
	// Offset and LineLimit page through a file by line. Without them a file
	// larger than the byte cap is simply unreadable past the cap, with no way to
	// reach the rest of it.
	Offset    int `json:"offset,omitempty" jsonschema:"minimum=1" desc:"1-based line to start reading from. Use with line_limit to page through a file that is larger than the byte cap."`
	LineLimit int `json:"line_limit,omitempty" jsonschema:"minimum=1,maximum=4000" desc:"Number of lines to return starting at offset. The result reports startLine, endLine, and whether more lines remain."`
}

func (ReadTool) Name() string { return "Read" }
func (ReadTool) Description() string {
	return "Read a file from the agent working directory. The first line reports file, hash, and line count for the whole file; pass that hash as expected_hash on Edit. Use offset (1-based start line) and line_limit to page through large files. Truncated reads without offset include line numbers and a cheap outline of declarations and headings."
}
func (ReadTool) Schema() any               { return readInput{} }
func (ReadTool) Risk(json.RawMessage) Risk { return RiskRead }

// ReplayClass is safe: reading a file twice yields a result and leaves no trace.
func (ReadTool) ReplayClass(json.RawMessage) ReplayClass { return ReplaySafe }

func (ReadTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input readInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	path, err := resolveInCWD(env.CWD, input.FilePath)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	limit := input.Limit
	if limit <= 0 || limit > maxReadBytes {
		limit = maxReadBytes
	}
	file, err := os.Open(path)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	// Images are answered from the whole file, so the byte limit that governs
	// text does not apply here.
	if mimeType, ok := sniffImageMIME(data); ok {
		if result, handled := readImageFile(file, path, mimeType); handled {
			return result, nil
		}
	}
	cut := len(data) > limit
	if isProbablyBinary(data, cut) {
		mimeType := strings.Split(http.DetectContentType(data), ";")[0]
		size := int64(len(data))
		if info, statErr := file.Stat(); statErr == nil {
			size = info.Size()
		}
		return Result{
			Output: fmt.Sprintf("Binary file not displayed (%s, %d bytes).", mimeType, size),
			Meta:   map[string]any{"path": path, "truncated": cut, "binary": true, "mime": mimeType, "size": size},
		}, nil
	}
	if cut {
		data = trimIncompleteUTF8(data[:limit])
	}
	display := editDiffDisplayPath(env.CWD, input.FilePath, path)
	if input.Offset > 0 || input.LineLimit > 0 {
		return readLineWindow(path, display, input, limit)
	}
	fp, err := textFileFingerprint(path, data, cut)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	text := string(data)
	if cut {
		text = numberTextLines(text, 1)
		text += "\n...[truncated]"
		outline := scanFileOutline(path, maxFileOutlineEntries+1)
		capped := len(outline) > maxFileOutlineEntries
		if capped {
			outline = outline[:maxFileOutlineEntries]
		}
		if formatted := formatFileOutline(outline, capped); formatted != "" {
			text += "\n" + formatted
		}
	}
	return withFileSnapshot(display, fp, text, map[string]any{"path": path, "truncated": cut, "binary": false}), nil
}

func textFileFingerprint(path string, window []byte, truncated bool) (fileFingerprint, error) {
	if !truncated {
		return hashBytes(window), nil
	}
	return hashFileOnDisk(path)
}

// sniffImageMIME reports the image type of data, limited to the formats a model
// can actually consume. Every other binary type keeps the plain binary report.
func sniffImageMIME(data []byte) (string, bool) {
	mimeType := strings.Split(http.DetectContentType(data), ";")[0]
	switch mimeType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return mimeType, true
	}
	return "", false
}

// readImageFile returns the image as base64 in Meta. It reports handled=false
// when the bytes sniff as an image but do not decode as one, so a corrupt or
// half-written file falls back to the binary report instead of shipping broken
// bytes to the model. The caller has only read the head of file, so this seeks
// back to the start.
func readImageFile(file *os.File, path, mimeType string) (Result, bool) {
	info, err := file.Stat()
	if err != nil {
		return Result{}, false
	}
	if info.Size() > maxReadImageBytes {
		return imageTooLargeResult(path, mimeType, info.Size()), true
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Result{}, false
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxReadImageBytes+1))
	if err != nil {
		return Result{}, false
	}
	// A file that grew between Stat and the read must not slip past the cap.
	if int64(len(raw)) > maxReadImageBytes {
		return imageTooLargeResult(path, mimeType, int64(len(raw))), true
	}
	width, height, decoded := imageDimensions(raw)
	// webp has no standard library decoder, so undecodable webp bytes are still
	// forwarded; for the other formats a failed header decode means corruption.
	if !decoded && mimeType != "image/webp" {
		return Result{}, false
	}
	size := int64(len(raw))
	description := fmt.Sprintf("Image: %s, %s", mimeType, formatByteSize(size))
	if decoded {
		description = fmt.Sprintf("Image: %s, %dx%d, %s", mimeType, width, height, formatByteSize(size))
	}
	meta := map[string]any{
		"path":      path,
		"truncated": false,
		"binary":    true,
		"mime":      mimeType,
		"size":      size,
		"image":     map[string]any{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(raw)},
	}
	if decoded {
		meta["width"] = width
		meta["height"] = height
	}
	return Result{Output: description, Meta: meta}, true
}

func imageTooLargeResult(path, mimeType string, size int64) Result {
	return Result{
		Output: fmt.Sprintf("Image not displayed: %s is %s, above the %s limit for inline images.", mimeType, formatByteSize(size), formatByteSize(maxReadImageBytes)),
		Meta: map[string]any{
			"path":          path,
			"truncated":     false,
			"binary":        true,
			"mime":          mimeType,
			"size":          size,
			"imageTooLarge": true,
		},
	}
}

func imageDimensions(data []byte) (int, int, bool) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return 0, 0, false
	}
	return config.Width, config.Height, true
}

func formatByteSize(size int64) string {
	switch {
	case size < 1024:
		return fmt.Sprintf("%d bytes", size)
	case size < 1<<20:
		return fmt.Sprintf("%.0f KB", float64(size)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	}
}

// readLineWindow returns a line range of a file so large files stay reachable
// past the byte cap. It streams rather than loading the whole file, and still
// bounds the returned bytes.
func readLineWindow(path, displayPath string, input readInput, byteLimit int) (Result, error) {
	fp, err := hashFileOnDisk(path)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	defer file.Close()

	start := input.Offset
	if start <= 0 {
		start = 1
	}
	lineLimit := input.LineLimit
	if lineLimit <= 0 || lineLimit > maxReadLines {
		lineLimit = maxReadLines
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), maxReadLineBytes)
	var builder strings.Builder
	lineNumber := 0
	emitted := 0
	truncated := false
	for scanner.Scan() {
		lineNumber++
		if lineNumber < start {
			continue
		}
		if emitted >= lineLimit {
			truncated = true
			break
		}
		line := scanner.Text()
		numbered := formatNumberedLine(lineNumber, line)
		if builder.Len()+len(numbered)+1 > byteLimit {
			truncated = true
			break
		}
		if emitted > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(numbered)
		emitted++
	}
	if err := scanner.Err(); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	text := builder.String()
	if emitted == 0 {
		text = fmt.Sprintf("No content: file has %d lines, requested start line %d.", fp.lines, start)
	} else if truncated {
		text += "\n...[truncated]"
	}
	meta := map[string]any{
		"path":      path,
		"truncated": truncated,
		"binary":    false,
		"startLine": start,
	}
	if emitted > 0 {
		meta["endLine"] = start + emitted - 1
		meta["windowLineCount"] = emitted
	}
	return withFileSnapshot(displayPath, fp, text, meta), nil
}

func isProbablyBinary(data []byte, truncated bool) bool {
	if len(data) == 0 {
		return false
	}
	if !utf8.Valid(data) && (!truncated || !hasOnlyIncompleteUTF8Suffix(data)) {
		return true
	}
	controls := 0
	for _, value := range data {
		if value == 0 {
			return true
		}
		if value < 0x20 && value != '\n' && value != '\r' && value != '\t' {
			controls++
		}
	}
	return controls*100 > len(data)*2
}

func hasOnlyIncompleteUTF8Suffix(data []byte) bool {
	start := max(0, len(data)-3)
	for index := start; index < len(data); index++ {
		if utf8.Valid(data[:index]) && !utf8.FullRune(data[index:]) {
			return true
		}
	}
	return false
}

func trimIncompleteUTF8(data []byte) []byte {
	if utf8.Valid(data) {
		return data
	}
	start := max(0, len(data)-3)
	for index := start; index < len(data); index++ {
		if utf8.Valid(data[:index]) && !utf8.FullRune(data[index:]) {
			return data[:index]
		}
	}
	return data
}
