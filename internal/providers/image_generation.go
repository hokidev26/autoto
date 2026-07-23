package providers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"strings"
)

const (
	maxImageGenerationBytes  = 10 << 20
	maxImageGenerationEdge   = 16_384
	maxImageGenerationPixels = 100_000_000
)

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

type imageGenerationTracker struct {
	emitted map[string]struct{}
	final   map[string]struct{}
	items   map[string]ImageGeneration
}

func newImageGenerationTracker() *imageGenerationTracker {
	return &imageGenerationTracker{
		emitted: make(map[string]struct{}),
		final:   make(map[string]struct{}),
		items:   make(map[string]ImageGeneration),
	}
}

type imageGenerationWireEvent struct {
	Type              string                  `json:"type"`
	ItemID            string                  `json:"item_id"`
	OutputIndex       int64                   `json:"output_index"`
	PartialImageB64   string                  `json:"partial_image_b64"`
	PartialImageIndex int64                   `json:"partial_image_index"`
	RevisedPrompt     string                  `json:"revised_prompt"`
	Item              imageGenerationWireItem `json:"item"`
	Response          struct {
		Output []imageGenerationWireItem `json:"output"`
	} `json:"response"`
}

type imageGenerationWireItem struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	Status        string `json:"status"`
	Result        string `json:"result"`
	RevisedPrompt string `json:"revised_prompt"`
}

func (t *imageGenerationTracker) processJSON(raw string) ([]Event, error) {
	var event imageGenerationWireEvent
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		return nil, errors.New("image generation event is invalid")
	}
	return t.process(event)
}

func (t *imageGenerationTracker) process(event imageGenerationWireEvent) ([]Event, error) {
	if t == nil {
		return nil, nil
	}
	switch event.Type {
	case "response.output_item.added":
		if event.Item.Type != "image_generation_call" {
			return nil, nil
		}
		return t.emitProgress(event.Item.ID, "added", event.OutputIndex, 0, event.Item.RevisedPrompt), nil
	case "response.image_generation_call.in_progress":
		return t.emitProgress(event.ItemID, "in_progress", event.OutputIndex, 0, event.RevisedPrompt), nil
	case "response.image_generation_call.generating":
		return t.emitProgress(event.ItemID, "generating", event.OutputIndex, 0, event.RevisedPrompt), nil
	case "response.image_generation_call.partial_image":
		if err := validatePartialImageBase64(event.PartialImageB64); err != nil {
			return nil, fmt.Errorf("invalid partial image generation result: %w", err)
		}
		return t.emitProgress(event.ItemID, "partial_image", event.OutputIndex, event.PartialImageIndex, event.RevisedPrompt), nil
	case "response.image_generation_call.completed":
		return t.emitProgress(event.ItemID, "completed", event.OutputIndex, 0, event.RevisedPrompt), nil
	case "response.output_item.done":
		if event.Item.Type != "image_generation_call" {
			return nil, nil
		}
		return t.emitFinal(event.Item, event.OutputIndex)
	case "response.completed", "response.incomplete", "response.failed":
		var events []Event
		for outputIndex, item := range event.Response.Output {
			if item.Type != "image_generation_call" {
				continue
			}
			generated, err := t.emitFinal(item, int64(outputIndex))
			if err != nil {
				return nil, err
			}
			events = append(events, generated...)
		}
		return events, nil
	default:
		return nil, nil
	}
}

func (t *imageGenerationTracker) emitProgress(id, status string, outputIndex, partialIndex int64, revisedPrompt string) []Event {
	id = strings.TrimSpace(id)
	status = strings.TrimSpace(status)
	if id == "" || status == "" {
		return nil
	}
	current := t.items[id]
	current.GenerationID = id
	current.Status = status
	current.OutputIndex = outputIndex
	current.PartialIndex = partialIndex
	if revisedPrompt = strings.TrimSpace(revisedPrompt); revisedPrompt != "" {
		current.RevisedPrompt = revisedPrompt
	}
	t.items[id] = current
	key := fmt.Sprintf("progress:%s:%s:%d:%d", id, status, outputIndex, partialIndex)
	if _, exists := t.emitted[key]; exists {
		return nil
	}
	t.emitted[key] = struct{}{}
	copy := current
	copy.Data = nil
	copy.MIME = ""
	copy.Width = 0
	copy.Height = 0
	return []Event{{Type: "image_generation", ImageGeneration: &copy}}
}

func (t *imageGenerationTracker) emitFinal(item imageGenerationWireItem, outputIndex int64) ([]Event, error) {
	id := strings.TrimSpace(item.ID)
	if id == "" || strings.TrimSpace(item.Result) == "" {
		return nil, nil
	}
	if _, exists := t.final[id]; exists {
		return nil, nil
	}
	data, width, height, err := decodeImageGenerationPNG(item.Result)
	if err != nil {
		return nil, fmt.Errorf("invalid image generation result for %q: %w", id, err)
	}
	current := t.items[id]
	current.GenerationID = id
	current.Status = strings.TrimSpace(item.Status)
	if current.Status == "" {
		current.Status = "completed"
	}
	current.OutputIndex = outputIndex
	if revisedPrompt := strings.TrimSpace(item.RevisedPrompt); revisedPrompt != "" {
		current.RevisedPrompt = revisedPrompt
	}
	current.Data = data
	current.MIME = "image/png"
	current.Width = width
	current.Height = height
	t.items[id] = current
	t.final[id] = struct{}{}
	return []Event{{Type: "image_generation", ImageGeneration: &current}}, nil
}

func validatePartialImageBase64(value string) error {
	_, err := decodeBoundedBase64(value)
	return err
}

func decodeImageGenerationPNG(value string) ([]byte, int, int, error) {
	data, err := decodeBoundedBase64(value)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(data) < len(pngSignature) || !bytes.Equal(data[:len(pngSignature)], pngSignature) {
		return nil, 0, 0, errors.New("result is not a PNG image")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, errors.New("PNG header is invalid")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageGenerationEdge || cfg.Height > maxImageGenerationEdge || int64(cfg.Width)*int64(cfg.Height) > maxImageGenerationPixels {
		return nil, 0, 0, errors.New("PNG dimensions are unreasonable")
	}
	return data, cfg.Width, cfg.Height, nil
}

func decodeBoundedBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("base64 result is empty")
	}
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		return nil, errors.New("data URLs are not accepted")
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return nil, errors.New("base64 result contains whitespace")
	}
	if len(value) > base64.StdEncoding.EncodedLen(maxImageGenerationBytes+1) {
		return nil, errors.New("decoded image exceeds 10 MiB")
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("base64 result is invalid")
	}
	if len(data) > maxImageGenerationBytes {
		return nil, errors.New("decoded image exceeds 10 MiB")
	}
	return data, nil
}
