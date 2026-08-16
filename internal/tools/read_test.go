package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadToolDoesNotRenderBinaryBytes(t *testing.T) {
	cwd := t.TempDir()
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0xff, 0x01}
	if err := os.WriteFile(filepath.Join(cwd, "image.png"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "binary", Name: "Read", Input: json.RawMessage(`{"file_path":"image.png"}`)}, Env{CWD: cwd})
	if err != nil || result.IsError {
		t.Fatalf("read failed: result=%+v err=%v", result, err)
	}
	if !strings.Contains(result.Output, "Binary file not displayed") || strings.Contains(result.Output, string(data)) {
		t.Fatalf("binary data was rendered directly: %q", result.Output)
	}
	if binary, _ := result.Meta["binary"].(bool); !binary || result.Meta["mime"] != "image/png" || result.Meta["size"] != int64(len(data)) {
		t.Fatalf("unexpected binary metadata: %+v", result.Meta)
	}
}

func TestReadToolReturnsImages(t *testing.T) {
	cwd := t.TempDir()
	tests := []struct {
		name     string
		file     string
		data     []byte
		mime     string
		wantDims string
	}{
		{name: "png", file: "shot.png", data: encodedTestImage(t, "png", 3, 2), mime: "image/png", wantDims: "3x2"},
		{name: "jpeg", file: "photo.jpg", data: encodedTestImage(t, "jpeg", 8, 4), mime: "image/jpeg", wantDims: "8x4"},
		{name: "gif", file: "anim.gif", data: encodedTestImage(t, "gif", 5, 7), mime: "image/gif", wantDims: "5x7"},
		{name: "webp has no stdlib decoder", file: "logo.webp", data: syntheticWebP(), mime: "image/webp"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(cwd, test.file)
			if err := os.WriteFile(path, test.data, 0o644); err != nil {
				t.Fatal(err)
			}
			result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "img", Name: "Read", Input: json.RawMessage(`{"file_path":` + quoteJSON(test.file) + `}`)}, Env{CWD: cwd})
			if err != nil || result.IsError {
				t.Fatalf("read failed: result=%+v err=%v", result, err)
			}
			if !strings.HasPrefix(result.Output, "Image: "+test.mime+", ") {
				t.Fatalf("unexpected description: %q", result.Output)
			}
			if test.wantDims != "" && !strings.Contains(result.Output, test.wantDims) {
				t.Fatalf("expected dimensions %s in %q", test.wantDims, result.Output)
			}
			if test.wantDims == "" && strings.Contains(result.Output, "x") {
				t.Fatalf("dimensions must be omitted when they cannot be decoded: %q", result.Output)
			}
			if result.Meta["mime"] != test.mime || result.Meta["size"] != int64(len(test.data)) {
				t.Fatalf("unexpected image metadata: %+v", result.Meta)
			}
			payload, ok := result.Meta["image"].(map[string]any)
			if !ok || payload["mimeType"] != test.mime {
				t.Fatalf("expected an image payload, got %+v", result.Meta["image"])
			}
			encoded, ok := payload["data"].(string)
			if !ok {
				t.Fatalf("expected base64 image data, got %T", payload["data"])
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || !bytes.Equal(decoded, test.data) {
				t.Fatalf("base64 payload did not round-trip the file bytes: err=%v", err)
			}
			if test.wantDims != "" {
				if _, ok := result.Meta["width"].(int); !ok {
					t.Fatalf("expected decoded dimensions in metadata: %+v", result.Meta)
				}
			} else if _, ok := result.Meta["width"]; ok {
				t.Fatalf("undecodable dimensions must be absent: %+v", result.Meta)
			}
		})
	}
}

func TestReadToolReturnsImageEvenWhenPagingArgumentsAreGiven(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "paged.png"), encodedTestImage(t, "png", 2, 2), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "paged", Name: "Read", Input: json.RawMessage(`{"file_path":"paged.png","offset":1,"line_limit":5}`)}, Env{CWD: cwd})
	if err != nil || result.IsError || !strings.HasPrefix(result.Output, "Image: image/png, 2x2,") {
		t.Fatalf("line paging must not defeat image reading: result=%+v err=%v", result, err)
	}
}

func TestReadToolRefusesOversizedImage(t *testing.T) {
	cwd := t.TempDir()
	oversized := append(encodedTestImage(t, "png", 2, 2), make([]byte, maxReadImageBytes+1)...)
	if err := os.WriteFile(filepath.Join(cwd, "huge.png"), oversized, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "huge", Name: "Read", Input: json.RawMessage(`{"file_path":"huge.png"}`)}, Env{CWD: cwd})
	if err != nil || result.IsError {
		t.Fatalf("read failed: result=%+v err=%v", result, err)
	}
	if !strings.Contains(result.Output, "Image not displayed") || !strings.Contains(result.Output, "4.0 MB limit") {
		t.Fatalf("expected a polite size refusal, got %q", result.Output)
	}
	if tooLarge, _ := result.Meta["imageTooLarge"].(bool); !tooLarge || result.Meta["image"] != nil {
		t.Fatalf("an oversized image must not carry a payload: %+v", result.Meta)
	}
}

func TestReadToolFallsBackToBinaryReportForUndecodableContent(t *testing.T) {
	cwd := t.TempDir()
	truncatedPNG := encodedTestImage(t, "png", 4, 4)
	truncatedPNG = truncatedPNG[:20]
	tests := []struct {
		name string
		file string
		data []byte
		mime string
	}{
		{name: "truncated png", file: "cut.png", data: truncatedPNG, mime: "image/png"},
		{name: "jpeg header with junk", file: "junk.jpg", data: append([]byte{0xff, 0xd8, 0xff}, bytes.Repeat([]byte{0x00, 0x7f}, 64)...), mime: "image/jpeg"},
		{name: "unsupported binary format", file: "doc.pdf", data: append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{0x00, 0x01}, 64)...), mime: "application/pdf"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(cwd, test.file), test.data, 0o644); err != nil {
				t.Fatal(err)
			}
			result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "bad", Name: "Read", Input: json.RawMessage(`{"file_path":` + quoteJSON(test.file) + `}`)}, Env{CWD: cwd})
			if err != nil || result.IsError || !strings.Contains(result.Output, "Binary file not displayed") {
				t.Fatalf("expected the binary report: result=%+v err=%v", result, err)
			}
			if result.Meta["image"] != nil || result.Meta["mime"] != test.mime {
				t.Fatalf("unexpected metadata: %+v", result.Meta)
			}
		})
	}
}

func encodedTestImage(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			source.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 0x40, A: 0xff})
		}
	}
	var buffer bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&buffer, source)
	case "jpeg":
		err = jpeg.Encode(&buffer, source, nil)
	case "gif":
		err = gif.Encode(&buffer, source, nil)
	default:
		t.Fatalf("unsupported test format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// syntheticWebP is the minimum RIFF container that content sniffing recognizes
// as webp; the standard library cannot decode webp at all, so real pixel data
// would buy the test nothing.
func syntheticWebP() []byte {
	return append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 32)...)
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestReadToolTreatsInvalidUTF8AsBinary(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "invalid.txt"), []byte{'o', 'k', 0xff, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "invalid", Name: "Read", Input: json.RawMessage(`{"file_path":"invalid.txt"}`)}, Env{CWD: cwd})
	if err != nil || result.IsError || !strings.Contains(result.Output, "Binary file not displayed") {
		t.Fatalf("expected invalid UTF-8 to be omitted as binary: result=%+v err=%v", result, err)
	}
}

func TestReadToolKeepsTruncatedUTF8Valid(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "unicode.txt"), []byte("界界"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "unicode", Name: "Read", Input: json.RawMessage(`{"file_path":"unicode.txt","limit":4}`)}, Env{CWD: cwd})
	if err != nil || result.IsError {
		t.Fatalf("read failed: result=%+v err=%v", result, err)
	}
	body := fileReadBody(result.Output)
	if binary, _ := result.Meta["binary"].(bool); binary || !result.Meta["truncated"].(bool) || !utf8.ValidString(result.Output) || !strings.Contains(body, "界") {
		t.Fatalf("expected valid truncated UTF-8 text: %+v", result)
	}
}
