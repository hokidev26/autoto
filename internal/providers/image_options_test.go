package providers

import "testing"

func TestResolveImageConfigDefaults(t *testing.T) {
	config, model := resolveImageConfig("gemini-3.1-flash-image", ImageOptions{})
	if model != "gemini-3.1-flash-image" {
		t.Fatalf("a suffix-free model must survive unchanged: %q", model)
	}
	if config["aspectRatio"] != defaultImageAspectRatio || config["imageSize"] != defaultImageSize {
		t.Fatalf("unexpected default image config: %+v", config)
	}
}

func TestResolveImageConfigFromOptions(t *testing.T) {
	for name, testCase := range map[string]struct {
		options   ImageOptions
		wantRatio string
		wantSize  string
	}{
		"explicit ratio":    {ImageOptions{Size: "16:9"}, "16:9", defaultImageSize},
		"pixel size":        {ImageOptions{Size: "1920x1080"}, "16:9", defaultImageSize},
		"portrait pixels":   {ImageOptions{Size: "1080x1920"}, "9:16", defaultImageSize},
		"quality hd":        {ImageOptions{Quality: "hd"}, defaultImageAspectRatio, "4K"},
		"quality standard":  {ImageOptions{Quality: "standard"}, defaultImageAspectRatio, "1K"},
		"image size wins":   {ImageOptions{Quality: "standard", ImageSize: "4k"}, defaultImageAspectRatio, "4K"},
		"ratio and quality": {ImageOptions{Size: "21:9", Quality: "2k"}, "21:9", "2K"},
		// An unsupported crop must not be forwarded: the upstream only accepts
		// the listed ratios, so guessing would turn a bad request into a
		// silently different image.
		"unsupported ratio": {ImageOptions{Size: "1000x3"}, defaultImageAspectRatio, defaultImageSize},
		"garbage size":      {ImageOptions{Size: "not-a-size"}, defaultImageAspectRatio, defaultImageSize},
	} {
		config, _ := resolveImageConfig("gemini-3.1-flash-image", testCase.options)
		if config["aspectRatio"] != testCase.wantRatio || config["imageSize"] != testCase.wantSize {
			t.Fatalf("%s: got %+v, want ratio=%s size=%s", name, config, testCase.wantRatio, testCase.wantSize)
		}
	}
}

func TestResolveImageConfigFromModelSuffix(t *testing.T) {
	for name, testCase := range map[string]struct {
		model     string
		wantModel string
		wantRatio string
		wantSize  string
	}{
		"ratio and size": {"gemini-3.1-flash-image-16x9-4k", "gemini-3.1-flash-image", "16:9", "4K"},
		"ratio only":     {"gemini-3.1-flash-image-9x16", "gemini-3.1-flash-image", "9:16", defaultImageSize},
		"size only":      {"gemini-3.1-flash-image-2k", "gemini-3.1-flash-image", defaultImageAspectRatio, "2K"},
		"hd alias":       {"gemini-3.1-flash-image-hd", "gemini-3.1-flash-image", defaultImageAspectRatio, "4K"},
		// "image" is part of the real slug and must never be mistaken for a
		// shorthand, or the upstream receives a model it does not know.
		"no suffix": {"gemini-3.1-flash-image", "gemini-3.1-flash-image", defaultImageAspectRatio, defaultImageSize},
	} {
		config, model := resolveImageConfig(testCase.model, ImageOptions{})
		if model != testCase.wantModel {
			t.Fatalf("%s: model = %q, want %q", name, model, testCase.wantModel)
		}
		if config["aspectRatio"] != testCase.wantRatio || config["imageSize"] != testCase.wantSize {
			t.Fatalf("%s: got %+v, want ratio=%s size=%s", name, config, testCase.wantRatio, testCase.wantSize)
		}
	}
}

// TestResolveImageConfigOptionsBeatSuffix pins the precedence: the model-name
// shorthand exists only for clients that cannot send structured parameters, so
// a client that sends both must get what it explicitly asked for.
func TestResolveImageConfigOptionsBeatSuffix(t *testing.T) {
	config, model := resolveImageConfig("gemini-3.1-flash-image-1x1-1k", ImageOptions{Size: "16:9", Quality: "hd"})
	if model != "gemini-3.1-flash-image" {
		t.Fatalf("suffixes must be stripped from the upstream model: %q", model)
	}
	if config["aspectRatio"] != "16:9" || config["imageSize"] != "4K" {
		t.Fatalf("explicit options did not win over the model suffix: %+v", config)
	}
}

func TestBuildGeminiCloudCodeImagePayload(t *testing.T) {
	payload := buildGeminiCloudCodePayload(GenerateRequest{
		Messages:     []Message{{Role: "user", Content: "a red circle"}},
		ImageOptions: ImageOptions{Size: "1920x1080", Quality: "hd"},
	}, "gemini-3.1-flash-image-1x1", "project-1", "")

	if payload["model"] != "gemini-3.1-flash-image" {
		t.Fatalf("upstream model still carries the shorthand suffix: %v", payload["model"])
	}
	inner, _ := payload["request"].(map[string]any)
	generation, _ := inner["generationConfig"].(map[string]any)
	imageConfig, _ := generation["imageConfig"].(map[string]any)
	if imageConfig["aspectRatio"] != "16:9" || imageConfig["imageSize"] != "4K" {
		t.Fatalf("image config was not derived from the request: %+v", imageConfig)
	}
	if generation["candidateCount"] != 1 {
		t.Fatalf("image requests must ask for exactly one candidate: %+v", generation)
	}
	safety, _ := inner["safetySettings"].([]map[string]any)
	if len(safety) != 4 {
		t.Fatalf("unexpected safety settings: %+v", inner["safetySettings"])
	}
	for _, setting := range safety {
		if setting["threshold"] != "OFF" {
			t.Fatalf("image safety threshold was not disabled: %+v", setting)
		}
	}
}
