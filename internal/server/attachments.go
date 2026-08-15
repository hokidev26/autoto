package server

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"autoto/internal/db"
	"autoto/internal/media"
)

const (
	maxAttachmentBytes        int64 = 10 << 20
	maxMessageUploadBytes     int64 = 25 << 20
	maxMessageModelImageBytes int64 = 8 << 20
	maxAttachmentsPerMessage        = 16
	maxImagesPerMessage             = 8
	maxAttachmentTextRunes          = 200000
	multipartMemoryBytes      int64 = 8 << 20
	maxDOCXArchiveEntries           = 256
	maxDOCXUncompressedBytes  int64 = 16 << 20
	maxDOCXDocumentBytes      int64 = 4 << 20
	maxDOCXTextBytes                = 1 << 20
)

type attachmentUploadError struct {
	Status  int
	Message string
}

func (e attachmentUploadError) Error() string { return e.Message }

func parseMultipartAttachments(w http.ResponseWriter, r *http.Request) (string, string, []db.Attachment, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMessageUploadBytes)
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	if err := r.ParseMultipartForm(multipartMemoryBytes); err != nil {
		return "", "", nil, attachmentUploadError{Status: http.StatusBadRequest, Message: fmt.Sprintf("附件上傳解析失敗：%v", err)}
	}
	text := strings.TrimSpace(r.FormValue("text"))
	createdBy := strings.TrimSpace(r.FormValue("createdBy"))
	files := multipartFiles(r.MultipartForm)
	if text == "" && len(files) == 0 {
		return "", "", nil, attachmentUploadError{Status: http.StatusBadRequest, Message: "text or files is required"}
	}
	if len(files) > maxAttachmentsPerMessage {
		return "", "", nil, attachmentUploadError{Status: http.StatusRequestEntityTooLarge, Message: "單則訊息附件數量超過 16 個限制"}
	}
	attachments := make([]db.Attachment, 0, len(files))
	var total int64
	var modelImageTotal int64
	imageCount := 0
	for _, header := range files {
		if header == nil {
			continue
		}
		if header.Size > maxAttachmentBytes {
			return "", "", nil, attachmentUploadError{Status: http.StatusRequestEntityTooLarge, Message: fmt.Sprintf("%s 超過 10 MB 限制", sanitizeAttachmentFilename(header.Filename))}
		}
		total += header.Size
		if total > maxMessageUploadBytes {
			return "", "", nil, attachmentUploadError{Status: http.StatusRequestEntityTooLarge, Message: "單則訊息附件總大小超過 25 MB"}
		}
		attachment, err := buildAttachmentFromPart(header)
		if err != nil {
			return "", "", nil, err
		}
		if attachment.Kind == "image" {
			imageCount++
			if imageCount > maxImagesPerMessage {
				return "", "", nil, attachmentUploadError{Status: http.StatusRequestEntityTooLarge, Message: "單則訊息圖片數量超過 8 張限制"}
			}
			modelImageTotal += int64(len(attachment.ModelData))
			if modelImageTotal > maxMessageModelImageBytes {
				return "", "", nil, attachmentUploadError{Status: http.StatusRequestEntityTooLarge, Message: "單則訊息規範化圖片總大小超過 8 MiB 限制"}
			}
		}
		attachments = append(attachments, attachment)
	}
	return text, createdBy, attachments, nil
}

func multipartFiles(form *multipart.Form) []*multipart.FileHeader {
	if form == nil || form.File == nil {
		return nil
	}
	out := make([]*multipart.FileHeader, 0)
	out = append(out, form.File["files"]...)
	out = append(out, form.File["files[]"]...)
	return out
}

func buildAttachmentFromPart(header *multipart.FileHeader) (db.Attachment, error) {
	file, err := header.Open()
	if err != nil {
		return db.Attachment{}, attachmentUploadError{Status: http.StatusBadRequest, Message: fmt.Sprintf("無法開啟附件 %s", sanitizeAttachmentFilename(header.Filename))}
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAttachmentBytes+1))
	if err != nil {
		return db.Attachment{}, attachmentUploadError{Status: http.StatusBadRequest, Message: fmt.Sprintf("無法讀取附件 %s", sanitizeAttachmentFilename(header.Filename))}
	}
	if int64(len(data)) > maxAttachmentBytes {
		return db.Attachment{}, attachmentUploadError{Status: http.StatusRequestEntityTooLarge, Message: fmt.Sprintf("%s 超過 10 MB 限制", sanitizeAttachmentFilename(header.Filename))}
	}
	filename := sanitizeAttachmentFilename(header.Filename)
	mimeType := normalizeAttachmentMIME(filename, header.Header.Get("Content-Type"), data)
	kind := classifyAttachment(filename, mimeType, data)
	attachment := db.Attachment{
		Filename:      filename,
		MIMEType:      mimeType,
		Kind:          kind,
		SizeBytes:     int64(len(data)),
		Data:          data,
		ExtractedText: extractAttachmentText(kind, filename, data),
	}
	if kind == "image" {
		processed := media.ProcessImage(data)
		attachment.ModelData = processed.ModelData
		attachment.ModelMIME = processed.ModelMIME
		attachment.Width = processed.Width
		attachment.Height = processed.Height
		attachment.SHA256 = processed.SHA256
		attachment.ProcessingStatus = processed.ProcessingStatus
		attachment.ProcessingCode = processed.ProcessingCode
		attachment.ProcessingError = processed.ProcessingError
	}
	return attachment, nil
}

func sanitizeAttachmentFilename(name string) string {
	name = strings.ToValidUTF8(name, "-")
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' || r == '\\' || r == ':' {
			return '-'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "." || name == ".." || name == "" {
		return "attachment"
	}
	for len(name) > 255 {
		name = name[:len(name)-1]
		for !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
	}
	return name
}

// normalizeAttachmentMIME deliberately prefers bytes over an untrusted multipart
// Content-Type or filename extension. This keeps HTML renamed to an image from
// being classified or served as an image.
func normalizeAttachmentMIME(filename, provided string, data []byte) string {
	if len(data) > 0 {
		detected := strings.Split(http.DetectContentType(data), ";")[0]
		if detected != "application/octet-stream" {
			return detected
		}
	}
	provided = strings.TrimSpace(strings.Split(provided, ";")[0])
	if provided != "" && provided != "application/octet-stream" {
		return provided
	}
	if extType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); extType != "" {
		return strings.Split(extType, ";")[0]
	}
	return "application/octet-stream"
}

func classifyAttachment(filename, mimeType string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(filename))
	mimeType = strings.ToLower(mimeType)
	if strings.HasPrefix(mimeType, "image/") {
		return "image"
	}
	if mimeType == "application/pdf" || (ext == ".pdf" && strings.HasPrefix(string(data), "%PDF-")) {
		return "pdf"
	}
	if (ext == ".docx" || strings.Contains(mimeType, "wordprocessingml.document")) && isZIPData(data) {
		return "docx"
	}
	if strings.HasPrefix(mimeType, "text/") || mimeType == "application/json" || mimeType == "application/xml" || knownTextExtension(ext) {
		return "text"
	}
	return "binary"
}

func isSafeInlineImage(mimeType string, data []byte) bool {
	if !media.IsSupportedImageMIME(mimeType) || media.DetectImageMIME(data) != mimeType {
		return false
	}
	return media.ProcessImage(data).ProcessingStatus == media.ProcessingReady
}

func isZIPData(data []byte) bool {
	return len(data) >= 4 && bytes.Equal(data[:4], []byte{'P', 'K', 3, 4})
}

func knownTextExtension(ext string) bool {
	switch ext {
	case ".txt", ".md", ".markdown", ".json", ".jsonl", ".csv", ".tsv", ".log", ".xml", ".yaml", ".yml", ".toml", ".ini", ".env", ".go", ".js", ".jsx", ".ts", ".tsx", ".css", ".html", ".htm", ".py", ".rb", ".rs", ".java", ".c", ".h", ".cpp", ".hpp", ".cs", ".php", ".sh", ".zsh", ".bash", ".sql", ".swift", ".kt", ".kts", ".dart", ".vue", ".svelte":
		return true
	default:
		return false
	}
}

func extractAttachmentText(kind, filename string, data []byte) string {
	switch kind {
	case "text":
		return truncateAttachmentText(decodeTextBytes(data))
	case "docx":
		text, err := extractDOCXText(data)
		if err != nil {
			return ""
		}
		return truncateAttachmentText(text)
	case "pdf":
		return truncateAttachmentText(extractPDFTextBestEffort(data))
	default:
		return ""
	}
}

func decodeTextBytes(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	return strings.ToValidUTF8(string(data), "�")
}

func truncateAttachmentText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxAttachmentTextRunes {
		return text
	}
	return string(runes[:maxAttachmentTextRunes]) + "\n\n[內容過長，已截斷。]"
}

func extractDOCXText(data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	if len(reader.File) > maxDOCXArchiveEntries {
		return "", errors.New("DOCX archive contains too many entries")
	}
	var document *zip.File
	var totalUncompressed uint64
	for _, file := range reader.File {
		totalUncompressed += file.UncompressedSize64
		if totalUncompressed > uint64(maxDOCXUncompressedBytes) {
			return "", errors.New("DOCX archive exceeds decompression budget")
		}
		if file.Name == "word/document.xml" {
			document = file
		}
	}
	if document == nil {
		return "", errors.New("word/document.xml not found")
	}
	if document.UncompressedSize64 > uint64(maxDOCXDocumentBytes) {
		return "", errors.New("DOCX document exceeds decompression budget")
	}
	file, err := document.Open()
	if err != nil {
		return "", err
	}
	defer file.Close()
	decoder := xml.NewDecoder(io.LimitReader(file, maxDOCXDocumentBytes+1))
	var builder strings.Builder
	inText := false
	lastWasNewline := false
	appendText := func(value string) bool {
		remaining := maxDOCXTextBytes - builder.Len()
		if remaining <= 0 {
			return false
		}
		if len(value) > remaining {
			value = value[:remaining]
		}
		builder.WriteString(value)
		lastWasNewline = strings.HasSuffix(value, "\n")
		return len(value) < remaining
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				if !appendText("\t") {
					return strings.TrimSpace(builder.String()), nil
				}
			case "br", "cr", "p":
				if builder.Len() > 0 && !lastWasNewline && !appendText("\n") {
					return strings.TrimSpace(builder.String()), nil
				}
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			}
		case xml.CharData:
			if inText && !appendText(string(t)) {
				return strings.TrimSpace(builder.String()), nil
			}
		}
	}
	return strings.TrimSpace(builder.String()), nil
}

var pdfLiteralPattern = regexp.MustCompile(`\((?:\\.|[^\\)]){2,}\)`)

func extractPDFTextBestEffort(data []byte) string {
	// 标准库无法可靠解析 PDF，尤其是压缩流或扫描件。这里仅尝试提取未压缩文字字面量；
	// 如果提取不到，后续会在 prompt 中明确提示该 PDF 需要支持文档/视觉/OCR 的模型。
	matches := pdfLiteralPattern.FindAll(data, 6000)
	if len(matches) == 0 {
		return ""
	}
	parts := make([]string, 0, len(matches))
	for _, match := range matches {
		decoded := decodePDFLiteral(match)
		decoded = strings.TrimSpace(html.UnescapeString(decoded))
		if decoded == "" || len([]rune(decoded)) < 2 {
			continue
		}
		parts = append(parts, decoded)
	}
	return strings.Join(parts, " ")
}

func decodePDFLiteral(raw []byte) string {
	if len(raw) >= 2 && raw[0] == '(' && raw[len(raw)-1] == ')' {
		raw = raw[1 : len(raw)-1]
	}
	var builder strings.Builder
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if ch != '\\' || i+1 >= len(raw) {
			builder.WriteByte(ch)
			continue
		}
		i++
		switch raw[i] {
		case 'n':
			builder.WriteByte('\n')
		case 'r':
			builder.WriteByte('\r')
		case 't':
			builder.WriteByte('\t')
		case 'b':
			builder.WriteByte('\b')
		case 'f':
			builder.WriteByte('\f')
		case '(', ')', '\\':
			builder.WriteByte(raw[i])
		default:
			builder.WriteByte(raw[i])
		}
	}
	return strings.ToValidUTF8(builder.String(), "�")
}
