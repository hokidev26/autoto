package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"
)

const maxReadBytes = 100000

type ReadTool struct{}

type readInput struct {
	FilePath string `json:"file_path"`
	Limit    int    `json:"limit,omitempty"`
}

func (ReadTool) Name() string              { return "Read" }
func (ReadTool) Description() string       { return "Read a file from the agent working directory." }
func (ReadTool) Schema() any               { return readInput{} }
func (ReadTool) Risk(json.RawMessage) Risk { return RiskRead }

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
	text := string(data)
	if cut {
		text += "\n...[truncated]"
	}
	return Result{Output: text, Meta: map[string]any{"path": path, "truncated": cut, "binary": false}}, nil
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
