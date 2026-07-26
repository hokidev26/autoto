package agent

import (
	"encoding/base64"
	"path/filepath"
	"strings"

	"autoto/internal/db"
	"autoto/internal/media"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

// maxToolResultImageBytes bounds the raw (pre-normalization) image bytes a tool
// result may contribute to a durable attachment. Read already caps the source
// file it will hand back at this same size, so this is a defense-in-depth
// ceiling against future tools handing back larger payloads and bloating the
// message store; media.ProcessImage separately bounds the normalized model
// bytes it produces to 4 MiB regardless.
const maxToolResultImageBytes = 4 << 20

// toolResultImageAttachment turns an image the Read tool placed in
// Result.Meta["image"] into a durable db.Attachment on the tool-result
// message, so it is rehydrated as a provider image block on every later turn
// via attachmentImageBlock (see runner_context.go). A tool result that is not
// an image, or an image that fails to decode, base64-decode, or normalize,
// yields ok=false so the caller falls back to the plain text tool result
// instead of failing the tool call.
func toolResultImageAttachment(agentID string, toolCall providers.ToolCall, rawResult tools.Result) (db.Attachment, bool) {
	if rawResult.IsError || rawResult.Meta == nil {
		return db.Attachment{}, false
	}
	imageMeta, ok := rawResult.Meta["image"].(map[string]any)
	if !ok {
		return db.Attachment{}, false
	}
	encoded, _ := imageMeta["data"].(string)
	if strings.TrimSpace(encoded) == "" {
		return db.Attachment{}, false
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw) > maxToolResultImageBytes {
		return db.Attachment{}, false
	}
	processed := media.ProcessImage(raw)
	if processed.ProcessingStatus != media.ProcessingReady {
		// Includes webp: media.ProcessImage has no webp decoder, so it always
		// rejects webp bytes rather than forwarding them past the model boundary.
		return db.Attachment{}, false
	}
	originalMIME, _ := imageMeta["mimeType"].(string)
	if strings.TrimSpace(originalMIME) == "" {
		originalMIME = processed.ModelMIME
	}
	return db.Attachment{
		AgentID:          agentID,
		Filename:         toolResultImageAttachmentFilename(rawResult, toolCall),
		MIMEType:         originalMIME,
		Kind:             "image",
		SizeBytes:        int64(len(raw)),
		Data:             raw,
		ModelData:        processed.ModelData,
		ModelMIME:        processed.ModelMIME,
		Width:            processed.Width,
		Height:           processed.Height,
		SHA256:           processed.SHA256,
		ProcessingStatus: processed.ProcessingStatus,
	}, true
}

// toolResultImageAttachmentFilename prefers the source path Read reported in
// Meta["path"], falling back to the tool name so the attachment always has a
// sensible display name even if a future image-producing tool omits path.
func toolResultImageAttachmentFilename(rawResult tools.Result, toolCall providers.ToolCall) string {
	if path, _ := rawResult.Meta["path"].(string); strings.TrimSpace(path) != "" {
		if base := filepath.Base(path); base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	name := strings.TrimSpace(toolCall.Name)
	if name == "" {
		return "image"
	}
	return name + "-image"
}
