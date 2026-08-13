package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"autoto/internal/db"
	"autoto/internal/providers"
)

// maxImagesPerRequest bounds n. The upstream serves one image per call, so n is
// implemented as n concurrent upstream calls — each one spending from an image
// quota that is metered weekly and small enough to exhaust in a handful of
// generations. A low ceiling keeps one request from consuming days of capacity.
const maxImagesPerRequest = 4

// maxImageEditParts bounds how many reference images an edit request may carry,
// independently of the body size limit, so a request cannot fan out into an
// unbounded number of decoded images held in memory at once.
const maxImageEditParts = 8

type imageGenerationRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              *int   `json:"n"`
	Size           string `json:"size"`
	Quality        string `json:"quality"`
	ImageSize      string `json:"image_size"`
	AspectRatio    string `json:"aspect_ratio"`
	ResponseFormat string `json:"response_format"`
	Style          string `json:"style"`
}

type imageDataItem struct {
	B64JSON       string `json:"b64_json,omitempty"`
	URL           string `json:"url,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

type imageResponse struct {
	Created int64           `json:"created"`
	Data    []imageDataItem `json:"data"`
}

func (s *Service) handleImageGenerations(w http.ResponseWriter, r *http.Request) {
	key, ok := s.authenticateRequest(w, r)
	if !ok {
		return
	}
	lease, err := s.limits.acquireIngress(key)
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusTooManyRequests, "concurrency_limit_exceeded", "Concurrency limit exceeded.", "rate_limit_error", "")
		return
	}
	defer lease.Release()

	var request imageGenerationRequest
	if problem := decodeJSONBody(w, r, s.maxRequestBytes, &request); problem != nil {
		writeProblem(w, problem)
		return
	}
	s.serveImageRequest(w, r, key, lease, request, nil)
}

func (s *Service) handleImageEdits(w http.ResponseWriter, r *http.Request) {
	key, ok := s.authenticateRequest(w, r)
	if !ok {
		return
	}
	lease, err := s.limits.acquireIngress(key)
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusTooManyRequests, "concurrency_limit_exceeded", "Concurrency limit exceeded.", "rate_limit_error", "")
		return
	}
	defer lease.Release()

	request, references, problem := decodeImageEditRequest(w, r, s.maxRequestBytes)
	if problem != nil {
		writeProblem(w, problem)
		return
	}
	s.serveImageRequest(w, r, key, lease, request, references)
}

// decodeImageEditRequest reads the multipart form OpenAI's edits endpoint uses.
// Any file part is treated as a reference image, not only "image" and "mask", so
// clients that send image1/image2/... to supply several references work without
// a bespoke field convention.
func decodeImageEditRequest(w http.ResponseWriter, r *http.Request, maxBytes int64) (imageGenerationRequest, []providers.ContentBlock, *apiProblem) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxRequestBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	if err := r.ParseMultipartForm(maxBytes); err != nil {
		return imageGenerationRequest{}, nil, invalidParam("body", "Request body must be a valid multipart form.")
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	request := imageGenerationRequest{
		Model:          r.FormValue("model"),
		Prompt:         r.FormValue("prompt"),
		Size:           r.FormValue("size"),
		Quality:        r.FormValue("quality"),
		ImageSize:      r.FormValue("image_size"),
		AspectRatio:    r.FormValue("aspect_ratio"),
		ResponseFormat: r.FormValue("response_format"),
		Style:          r.FormValue("style"),
	}
	if raw := strings.TrimSpace(r.FormValue("n")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return imageGenerationRequest{}, nil, invalidParam("n", "n must be an integer.")
		}
		request.N = &parsed
	}

	references := make([]providers.ContentBlock, 0, maxImageEditParts)
	if r.MultipartForm != nil {
		// Field order in a multipart form is not preserved by the parser, so
		// sort by field name to make the reference order deterministic — image1
		// before image2 — rather than dependent on map iteration.
		names := make([]string, 0, len(r.MultipartForm.File))
		for name := range r.MultipartForm.File {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			for _, header := range r.MultipartForm.File[name] {
				if len(references) >= maxImageEditParts {
					return imageGenerationRequest{}, nil, invalidParam("image", fmt.Sprintf("At most %d reference images are supported.", maxImageEditParts))
				}
				block, problem := imageBlockFromPart(header)
				if problem != nil {
					return imageGenerationRequest{}, nil, problem
				}
				references = append(references, block)
			}
		}
	}
	if len(references) == 0 {
		return imageGenerationRequest{}, nil, invalidParam("image", "At least one reference image is required.")
	}
	return request, references, nil
}

func imageBlockFromPart(header *multipart.FileHeader) (providers.ContentBlock, *apiProblem) {
	file, err := header.Open()
	if err != nil {
		return providers.ContentBlock{}, invalidParam("image", "A reference image could not be read.")
	}
	defer file.Close()
	data := make([]byte, header.Size)
	if _, err := io.ReadFull(file, data); err != nil {
		return providers.ContentBlock{}, invalidParam("image", "A reference image could not be read.")
	}
	mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		// The declared type is client-supplied and often absent or wrong; fall
		// back rather than rejecting a genuine image over a bad header.
		mimeType = "image/png"
	}
	return providers.ContentBlock{Type: "image", MIMEType: mimeType, Data: data}, nil
}

func (s *Service) serveImageRequest(w http.ResponseWriter, r *http.Request, key db.GatewayKey, lease *ingressLease, request imageGenerationRequest, references []providers.ContentBlock) {
	count, providerRequest, problem := convertImageRequest(request, references)
	if problem != nil {
		writeProblem(w, problem)
		return
	}
	resolved, problem := s.prepareProviderRequest(r.Context(), key, request.Model, &providerRequest, len(references) > 0, lease, generationParameterNames{
		Images: "image",
	})
	if problem != nil {
		writeProblem(w, problem)
		return
	}

	results := make([][]imageDataItem, count)
	failures := make([]string, count)
	var waitGroup sync.WaitGroup
	for index := 0; index < count; index++ {
		waitGroup.Add(1)
		go func(slot int) {
			defer waitGroup.Done()
			items, execution, err := s.generateOneImage(r.Context(), resolved, providerRequest, request.ResponseFormat)
			s.recordGatewayRequest(key, resolved, execution)
			if err != nil {
				failures[slot] = err.Error()
				return
			}
			results[slot] = items
		}(index)
	}
	waitGroup.Wait()

	data := make([]imageDataItem, 0, count)
	for _, items := range results {
		data = append(data, items...)
	}
	if len(data) == 0 {
		// Every attempt failed. Report the first reason so a quota or policy
		// rejection is distinguishable from a generic upstream outage.
		message := "The upstream image request failed."
		for _, failure := range failures {
			if failure != "" {
				message = failure
				break
			}
		}
		writeAPIError(w, http.StatusBadGateway, "upstream_error", message, "server_error", "")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(imageResponse{Created: s.now().Unix(), Data: data})
}

func (s *Service) generateOneImage(ctx context.Context, resolved resolvedModel, request providers.GenerateRequest, responseFormat string) ([]imageDataItem, completionExecution, error) {
	execution := completionExecution{StartedAt: time.Now()}
	events, err := resolved.Provider.Generate(ctx, request)
	if err != nil {
		execution.ErrorMessage = gatewayFailureUpstreamStart
		return nil, execution, err
	}
	if events == nil {
		execution.ErrorMessage = gatewayFailureProviderNoEventFeed
		return nil, execution, errors.New("the upstream model request failed")
	}
	items := make([]imageDataItem, 0, 1)
	for {
		select {
		case <-ctx.Done():
			execution.ErrorMessage = gatewayFailureRequestCanceled
			return nil, execution, ctx.Err()
		case event, ok := <-events:
			if !ok {
				if len(items) > 0 {
					return items, execution, nil
				}
				execution.ErrorMessage = gatewayFailureUpstreamEnded
				return nil, execution, errors.New("the upstream model request failed")
			}
			captureExecutionEvent(&execution, event)
			switch event.Type {
			case "image_generation":
				// Only completed images carry bytes; progress events for the
				// same generation arrive first and must not become results.
				if event.ImageGeneration == nil || len(event.ImageGeneration.Data) == 0 {
					continue
				}
				execution.markOutput()
				items = append(items, imageDataItemFrom(*event.ImageGeneration, responseFormat))
			case "error":
				execution.ErrorMessage = gatewayFailureUpstreamEvent
				return nil, execution, fmt.Errorf("%s", event.Text)
			case "done":
				if len(items) == 0 {
					execution.ErrorMessage = gatewayFailureUpstreamEnded
					return nil, execution, errors.New("the upstream returned no image")
				}
				return items, execution, nil
			}
		}
	}
}

func imageDataItemFrom(image providers.ImageGeneration, responseFormat string) imageDataItem {
	encoded := base64.StdEncoding.EncodeToString(image.Data)
	item := imageDataItem{RevisedPrompt: image.RevisedPrompt}
	if strings.EqualFold(strings.TrimSpace(responseFormat), "url") {
		mimeType := strings.TrimSpace(image.MIME)
		if mimeType == "" {
			mimeType = "image/png"
		}
		// Autoto does not host generated images, so "url" is served as a data
		// URI: it satisfies clients that render whatever the field contains,
		// without inventing an endpoint that would have to outlive the request.
		item.URL = "data:" + mimeType + ";base64," + encoded
		return item
	}
	item.B64JSON = encoded
	return item
}

func convertImageRequest(request imageGenerationRequest, references []providers.ContentBlock) (int, providers.GenerateRequest, *apiProblem) {
	if strings.TrimSpace(request.Model) == "" {
		return 0, providers.GenerateRequest{}, invalidParam("model", "A model is required.")
	}
	prompt := strings.TrimSpace(request.Prompt)
	if prompt == "" {
		return 0, providers.GenerateRequest{}, invalidParam("prompt", "A prompt is required.")
	}
	if style := strings.TrimSpace(request.Style); style != "" {
		prompt = prompt + ", " + style
	}
	count := 1
	if request.N != nil {
		count = *request.N
		if count < 1 || count > maxImagesPerRequest {
			return 0, providers.GenerateRequest{}, invalidParam("n", fmt.Sprintf("n must be between 1 and %d.", maxImagesPerRequest))
		}
	}
	switch strings.ToLower(strings.TrimSpace(request.ResponseFormat)) {
	case "", "b64_json", "url":
	default:
		return 0, providers.GenerateRequest{}, invalidParam("response_format", "response_format must be b64_json or url.")
	}

	blocks := make([]providers.ContentBlock, 0, len(references)+1)
	blocks = append(blocks, providers.ContentBlock{Type: "text", Text: prompt})
	blocks = append(blocks, references...)

	// aspect_ratio takes precedence over size, matching the edits form where a
	// caller can send both and means the explicit ratio.
	size := strings.TrimSpace(request.AspectRatio)
	if size == "" {
		size = strings.TrimSpace(request.Size)
	}
	return count, providers.GenerateRequest{
		Messages:              []providers.Message{{Role: "user", Content: prompt, Blocks: blocks}},
		EnableImageGeneration: true,
		ImageOptions: providers.ImageOptions{
			Size:      size,
			Quality:   strings.TrimSpace(request.Quality),
			ImageSize: strings.TrimSpace(request.ImageSize),
		},
	}, nil
}
