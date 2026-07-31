package providers

import (
	"math"
	"strconv"
	"strings"
)

// ImageOptions carries the framing and detail an image request asks for. The
// fields mirror the OpenAI Images API so a gateway request can be passed through
// unchanged; each is optional and falls back to the defaults below.
type ImageOptions struct {
	// Size is either an explicit ratio ("16:9") or a pixel size ("1920x1080"),
	// which is normalized to the nearest supported ratio.
	Size string
	// Quality is the OpenAI spelling: standard/medium/hd, or 1k/2k/4k.
	Quality string
	// ImageSize is an explicit "1K"/"2K"/"4K" and wins over Quality.
	ImageSize string
}

const (
	defaultImageAspectRatio = "1:1"
	defaultImageSize        = "2K"
	// aspectRatioTolerance is how far a pixel size may sit from a supported
	// ratio and still map to it. 0.05 accepts the common screen sizes — 1920x1080
	// is 1.778 against 16:9's 1.778, 1366x768 is 1.779 — without letting an
	// unusual crop silently become something the caller did not ask for.
	aspectRatioTolerance = 0.05
)

// supportedImageAspectRatios are the ratios the upstream image models accept.
// A pixel size that matches none of them within the tolerance falls back to the
// default rather than being passed through, because the upstream rejects ratios
// outside this set.
var supportedImageAspectRatios = []struct {
	name  string
	value float64
}{
	{"21:9", 21.0 / 9.0},
	{"16:9", 16.0 / 9.0},
	{"3:2", 3.0 / 2.0},
	{"5:4", 5.0 / 4.0},
	{"4:3", 4.0 / 3.0},
	{"1:1", 1.0},
	{"4:5", 4.0 / 5.0},
	{"3:4", 3.0 / 4.0},
	{"2:3", 2.0 / 3.0},
	{"9:16", 9.0 / 16.0},
}

// imageModelSuffixes are the ratio and size shorthands a model name may carry,
// for callers that cannot send structured parameters. Longest first so "-16x9"
// is not shadowed by a shorter match.
var imageModelSuffixes = []struct {
	suffix      string
	aspectRatio string
	imageSize   string
}{
	{"-21x9", "21:9", ""},
	{"-16x9", "16:9", ""},
	{"-9x16", "9:16", ""},
	{"-4x3", "4:3", ""},
	{"-3x4", "3:4", ""},
	{"-3x2", "3:2", ""},
	{"-2x3", "2:3", ""},
	{"-5x4", "5:4", ""},
	{"-4x5", "4:5", ""},
	{"-1x1", "1:1", ""},
	{"-4k", "", "4K"},
	{"-2k", "", "2K"},
	{"-1k", "", "1K"},
	{"-hd", "", "4K"},
}

// resolveImageConfig turns a model name and request options into the upstream
// imageConfig, and returns the model name with any shorthand suffix removed.
//
// Precedence runs explicit options over model-name shorthand, because the
// shorthand exists only for clients that cannot send structured parameters.
func resolveImageConfig(model string, options ImageOptions) (map[string]any, string) {
	cleanModel, suffixRatio, suffixSize := stripImageModelSuffixes(model)

	aspectRatio := suffixRatio
	if ratio := aspectRatioFromSize(options.Size); ratio != "" {
		aspectRatio = ratio
	}
	if aspectRatio == "" {
		aspectRatio = defaultImageAspectRatio
	}

	imageSize := suffixSize
	if size := normalizeImageSize(options.Quality); size != "" {
		imageSize = size
	}
	if size := normalizeImageSize(options.ImageSize); size != "" {
		imageSize = size
	}
	if imageSize == "" {
		imageSize = defaultImageSize
	}

	return map[string]any{"aspectRatio": aspectRatio, "imageSize": imageSize}, cleanModel
}

func stripImageModelSuffixes(model string) (string, string, string) {
	name := strings.TrimSpace(model)
	aspectRatio := ""
	imageSize := ""
	for stripped := true; stripped; {
		stripped = false
		for _, candidate := range imageModelSuffixes {
			if len(name) <= len(candidate.suffix) || !strings.HasSuffix(strings.ToLower(name), candidate.suffix) {
				continue
			}
			name = name[:len(name)-len(candidate.suffix)]
			// Keep the outermost suffix: "-16x9-4k" is read right to left, and a
			// caller repeating a dimension means the last one written wins.
			if candidate.aspectRatio != "" && aspectRatio == "" {
				aspectRatio = candidate.aspectRatio
			}
			if candidate.imageSize != "" && imageSize == "" {
				imageSize = candidate.imageSize
			}
			stripped = true
			break
		}
	}
	return name, aspectRatio, imageSize
}

// aspectRatioFromSize accepts either a ratio ("16:9") or a pixel size
// ("1920x1080"). It returns "" when the input names nothing usable, leaving the
// caller's fallback in charge.
func aspectRatioFromSize(size string) string {
	size = strings.ToLower(strings.TrimSpace(size))
	if size == "" || size == "auto" {
		return ""
	}
	if strings.Contains(size, ":") {
		for _, candidate := range supportedImageAspectRatios {
			if size == candidate.name {
				return candidate.name
			}
		}
		return ""
	}
	width, height, found := strings.Cut(size, "x")
	if !found {
		return ""
	}
	parsedWidth, widthErr := strconv.ParseFloat(strings.TrimSpace(width), 64)
	parsedHeight, heightErr := strconv.ParseFloat(strings.TrimSpace(height), 64)
	if widthErr != nil || heightErr != nil || parsedWidth <= 0 || parsedHeight <= 0 {
		return ""
	}
	ratio := parsedWidth / parsedHeight
	best := ""
	bestDelta := math.MaxFloat64
	for _, candidate := range supportedImageAspectRatios {
		if delta := math.Abs(ratio - candidate.value); delta < bestDelta {
			best, bestDelta = candidate.name, delta
		}
	}
	if bestDelta > aspectRatioTolerance {
		return ""
	}
	return best
}

func normalizeImageSize(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hd", "4k":
		return "4K"
	case "medium", "2k":
		return "2K"
	case "standard", "1k":
		return "1K"
	default:
		return ""
	}
}
