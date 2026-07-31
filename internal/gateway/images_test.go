package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autoto/internal/db"
	"autoto/internal/providers"
)

func imageTestProvider(images ...[]byte) *gatewayTestProvider {
	events := make([]providers.Event, 0, len(images)+2)
	// A progress event with no bytes arrives before the finished image; it must
	// never be counted as a result.
	events = append(events, providers.Event{Type: "image_generation", ImageGeneration: &providers.ImageGeneration{
		GenerationID: "ig_1", Status: "generating",
	}})
	for index, data := range images {
		events = append(events, providers.Event{Type: "image_generation", ImageGeneration: &providers.ImageGeneration{
			GenerationID:  "ig_1",
			Status:        "completed",
			Data:          data,
			MIME:          "image/png",
			RevisedPrompt: "revised",
			OutputIndex:   int64(index),
		}})
	}
	events = append(events, providers.Event{Type: "done", Done: true, StopReason: "stop"})
	return &gatewayTestProvider{
		name:         "backend",
		capabilities: providers.Capabilities{Streaming: true, ImageInput: true, ImageGeneration: true},
		modelCaps:    providers.ModelCapabilities{ImageGeneration: true, ImageGenerationKnown: true},
		events:       events,
	}
}

func decodeImageResponse(t *testing.T, recorder *httptest.ResponseRecorder) imageResponse {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response imageResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestGatewayImageGenerationReturnsBase64AndPassesOptions(t *testing.T) {
	pixels := []byte("fake-png-bytes")
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true}, imageTestProvider(pixels), nil)

	recorder := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/images/generations",
		`{"model":"shared","prompt":"a red circle","size":"1920x1080","quality":"hd"}`)
	response := decodeImageResponse(t, recorder)

	if len(response.Data) != 1 {
		t.Fatalf("expected one image, got %+v", response.Data)
	}
	if response.Data[0].B64JSON != base64.StdEncoding.EncodeToString(pixels) {
		t.Fatalf("image bytes were not returned as b64_json: %+v", response.Data[0])
	}
	if response.Data[0].URL != "" {
		t.Fatalf("b64_json responses must not also carry a url: %+v", response.Data[0])
	}
	request := harness.provider.lastRequest()
	if !request.EnableImageGeneration {
		t.Fatal("image generation was not enabled on the provider request")
	}
	if request.ImageOptions.Size != "1920x1080" || request.ImageOptions.Quality != "hd" {
		t.Fatalf("image options were not forwarded: %+v", request.ImageOptions)
	}
}

func TestGatewayImageGenerationURLFormatReturnsDataURI(t *testing.T) {
	pixels := []byte("fake-png-bytes")
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true}, imageTestProvider(pixels), nil)

	recorder := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/images/generations",
		`{"model":"shared","prompt":"a red circle","response_format":"url"}`)
	response := decodeImageResponse(t, recorder)

	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pixels)
	if response.Data[0].URL != want {
		t.Fatalf("url format did not produce a data URI: %+v", response.Data[0])
	}
	if response.Data[0].B64JSON != "" {
		t.Fatalf("url responses must not also carry b64_json: %+v", response.Data[0])
	}
}

// TestGatewayImageGenerationFansOutForN covers the reason n is implemented as
// concurrent calls: the upstream serves one image per request.
func TestGatewayImageGenerationFansOutForN(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true}, imageTestProvider([]byte("one")), nil)

	recorder := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/images/generations",
		`{"model":"shared","prompt":"a red circle","n":3}`)
	response := decodeImageResponse(t, recorder)

	if len(response.Data) != 3 {
		t.Fatalf("expected three images, got %d", len(response.Data))
	}
	if got := harness.provider.requestCount(); got != 3 {
		t.Fatalf("expected three upstream calls for n=3, got %d", got)
	}
}

// TestGatewayImageGenerationRejectsUnsupportedParameters keeps a request that
// would silently cost quota from reaching the upstream at all.
func TestGatewayImageGenerationRejectsUnsupportedParameters(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true}, imageTestProvider([]byte("one")), nil)

	for name, body := range map[string]string{
		"missing prompt":        `{"model":"shared"}`,
		"missing model":         `{"prompt":"a red circle"}`,
		"n below range":         `{"model":"shared","prompt":"x","n":0}`,
		"n above range":         `{"model":"shared","prompt":"x","n":99}`,
		"bad response_format":   `{"model":"shared","prompt":"x","response_format":"png"}`,
		"trailing json objects": `{"model":"shared","prompt":"x"}{}`,
	} {
		recorder := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/images/generations", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", name, recorder.Code, recorder.Body.String())
		}
	}
	if got := harness.provider.requestCount(); got != 0 {
		t.Fatalf("rejected requests must not reach the upstream, got %d calls", got)
	}
}

func TestGatewayImageEditsCarriesReferencesInOrder(t *testing.T) {
	pixels := []byte("fake-png-bytes")
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true}, imageTestProvider(pixels), nil)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("model", "shared")
	_ = writer.WriteField("prompt", "make it blue")
	_ = writer.WriteField("aspect_ratio", "16:9")
	_ = writer.WriteField("image_size", "4K")
	// Written out of order to prove the handler sorts by field name rather than
	// depending on multipart map iteration.
	for _, name := range []string{"image2", "image1"} {
		part, err := writer.CreateFormFile(name, name+".png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("ref-" + name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	request.Header.Set("Authorization", "Bearer "+harness.generated.Token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	harness.service.ServeHTTP(recorder, request)
	decodeImageResponse(t, recorder)

	providerRequest := harness.provider.lastRequest()
	if providerRequest.ImageOptions.Size != "16:9" || providerRequest.ImageOptions.ImageSize != "4K" {
		t.Fatalf("edit options were not forwarded: %+v", providerRequest.ImageOptions)
	}
	blocks := providerRequest.Messages[0].Blocks
	if len(blocks) != 3 || blocks[0].Type != "text" {
		t.Fatalf("unexpected edit blocks: %+v", blocks)
	}
	if string(blocks[1].Data) != "ref-image1" || string(blocks[2].Data) != "ref-image2" {
		t.Fatalf("reference images are not in field-name order: %q, %q", blocks[1].Data, blocks[2].Data)
	}
}

func TestGatewayImageEditsRequiresAReferenceImage(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true}, imageTestProvider([]byte("one")), nil)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	_ = writer.WriteField("model", "shared")
	_ = writer.WriteField("prompt", "make it blue")
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	request.Header.Set("Authorization", "Bearer "+harness.generated.Token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	harness.service.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGatewayImageEndpointsRejectNonPostAndAnonymous(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true}, imageTestProvider([]byte("one")), nil)

	for _, path := range []string{"/v1/images/generations", "/v1/images/edits"} {
		recorder := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodGet, path, "")
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: expected 405, got %d", path, recorder.Code)
		}
		if allow := recorder.Header().Get("Allow"); !strings.Contains(allow, http.MethodPost) {
			t.Fatalf("%s: missing Allow header: %q", path, allow)
		}
		anonymous := gatewayRequest(t, harness.service, "", http.MethodPost, path, `{"model":"shared","prompt":"x"}`)
		if anonymous.Code != http.StatusUnauthorized {
			t.Fatalf("%s: expected 401 without a key, got %d", path, anonymous.Code)
		}
	}
}
