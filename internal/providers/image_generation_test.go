package providers

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestImageGenerationTrackerEmitsProgressAndDeduplicatesFinalImage(t *testing.T) {
	pngData := testImageGenerationPNG(t, 2, 3)
	encoded := base64.StdEncoding.EncodeToString(pngData)
	tracker := newImageGenerationTracker()
	rawEvents := []string{
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"image_generation_call","id":"ig_1","status":"in_progress","revised_prompt":"draw a lighthouse"}}`,
		`{"type":"response.image_generation_call.in_progress","item_id":"ig_1","output_index":1}`,
		`{"type":"response.image_generation_call.generating","item_id":"ig_1","output_index":1}`,
		`{"type":"response.image_generation_call.partial_image","item_id":"ig_1","output_index":1,"partial_image_index":0,"partial_image_b64":"aGVsbG8="}`,
		`{"type":"response.image_generation_call.completed","item_id":"ig_1","output_index":1}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"image_generation_call","id":"ig_1","status":"completed","result":"` + encoded + `"}}`,
		`{"type":"response.completed","response":{"output":[{"type":"image_generation_call","id":"ig_1","status":"completed","result":"` + encoded + `"}]}}`,
	}
	var events []Event
	for _, raw := range rawEvents {
		generated, err := tracker.processJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, generated...)
	}
	if len(events) != 6 {
		t.Fatalf("expected five progress events and one final event, got %+v", events)
	}
	statuses := make([]string, 0, len(events))
	finalCount := 0
	for _, event := range events {
		if event.Type != "image_generation" || event.ImageGeneration == nil || event.ToolCall != nil {
			t.Fatalf("image generation leaked into the tool-call contract: %+v", event)
		}
		imageEvent := event.ImageGeneration
		statuses = append(statuses, imageEvent.Status)
		if len(imageEvent.Data) > 0 {
			finalCount++
			if imageEvent.GenerationID != "ig_1" || imageEvent.OutputIndex != 1 || imageEvent.RevisedPrompt != "draw a lighthouse" || imageEvent.MIME != "image/png" || imageEvent.Width != 2 || imageEvent.Height != 3 || !bytes.Equal(imageEvent.Data, pngData) {
				t.Fatalf("unexpected final image event: %+v", imageEvent)
			}
		}
	}
	if strings.Join(statuses, ",") != "added,in_progress,generating,partial_image,completed,completed" || finalCount != 1 {
		t.Fatalf("unexpected image event sequence: statuses=%v finals=%d", statuses, finalCount)
	}
	if len(events[3].ImageGeneration.Data) != 0 {
		t.Fatal("partial image bytes must not be retained on the event")
	}
}

func TestImageGenerationTrackerUsesIncompleteResponseAsFinalFallback(t *testing.T) {
	pngData := testImageGenerationPNG(t, 2, 2)
	encoded := base64.StdEncoding.EncodeToString(pngData)
	tracker := newImageGenerationTracker()
	events, err := tracker.processJSON(`{"type":"response.incomplete","response":{"output":[{"type":"image_generation_call","id":"ig_incomplete","status":"completed","result":"` + encoded + `"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ImageGeneration == nil || !bytes.Equal(events[0].ImageGeneration.Data, pngData) {
		t.Fatalf("incomplete response did not preserve its final generated image: %+v", events)
	}
}

func TestImageGenerationValidationRejectsUnsafeResults(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "data URL", value: "data:image/png;base64,aGVsbG8=", want: "data URLs"},
		{name: "invalid base64", value: "%%%", want: "invalid"},
		{name: "non PNG", value: base64.StdEncoding.EncodeToString([]byte("not-png")), want: "not a PNG"},
		{name: "oversized", value: base64.StdEncoding.EncodeToString(make([]byte, maxImageGenerationBytes+1)), want: "10 MiB"},
		{name: "unreasonable dimensions", value: base64.StdEncoding.EncodeToString(testImageGenerationPNG(t, maxImageGenerationEdge+1, 1)), want: "dimensions"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := decodeImageGenerationPNG(test.value); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestImageGenerationContentBlockJSONKeepsMetadataButNotData(t *testing.T) {
	block := ContentBlock{
		Type:          "image_generation",
		GenerationID:  "ig_1",
		Status:        "completed",
		OutputIndex:   2,
		PartialIndex:  1,
		RevisedPrompt: "revised",
		MIMEType:      "image/png",
		Width:         4,
		Height:        5,
		Data:          []byte("secret image bytes"),
	}
	encoded, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, required := range []string{`"type":"image_generation"`, `"generationId":"ig_1"`, `"status":"completed"`, `"outputIndex":2`, `"partialIndex":1`, `"revisedPrompt":"revised"`, `"mimeType":"image/png"`, `"width":4`, `"height":5`} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing %s from content block JSON: %s", required, text)
		}
	}
	if strings.Contains(text, "secret image bytes") || strings.Contains(text, "c2VjcmV0") {
		t.Fatalf("temporary image data leaked into content block JSON: %s", text)
	}
}

func testImageGenerationPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 12, G: 34, B: 56, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
