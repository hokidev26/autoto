package themes

import "math"

// ContrastWarning reports one token pair whose contrast falls below the
// minimum a reader needs. Warnings are advisory: an import still succeeds, but
// the person installing the theme learns which text will be hard to read
// before they activate it rather than after.
type ContrastWarning struct {
	Pair       string  `json:"pair"`
	Foreground string  `json:"foreground"`
	Background string  `json:"background"`
	Ratio      float64 `json:"ratio"`
	Minimum    float64 `json:"minimum"`
}

// Body text follows WCAG AA for normal text; muted text and the primary accent
// are held to the AA large-text/UI-component threshold instead, because muted
// is secondary by design and primary mostly colors controls, not prose.
const (
	contrastMinimumText   = 4.5
	contrastMinimumMuted  = 3.0
	contrastMinimumAccent = 3.0
)

// AuditContrast scores the readable token pairs of a validated manifest and
// returns the ones below minimum. The dark variant palette is audited as its
// own surface set, since it is exactly what dark mode will render.
func AuditContrast(manifest Manifest) []ContrastWarning {
	warnings := auditTokenContrast("", manifest.Tokens)
	if manifest.DarkTokens != nil {
		warnings = append(warnings, auditTokenContrast("dark ", *manifest.DarkTokens)...)
	}
	return warnings
}

func auditTokenContrast(prefix string, tokens Tokens) []ContrastWarning {
	// Translucent tokens render over the canvas, and the canvas over the page
	// default, so ratios are measured against the composited result the reader
	// actually sees rather than the raw hex values.
	white := rgbaColor{r: 1, g: 1, b: 1, a: 1}
	canvas := compositeColor(parseHexRGBA(tokens.Canvas), white)
	card := compositeColor(parseHexRGBA(tokens.Card), canvas)
	input := compositeColor(parseHexRGBA(tokens.Input), canvas)
	sidebar := compositeColor(parseHexRGBA(tokens.Sidebar), canvas)
	checks := []struct {
		pair       string
		foreground string
		background string
		surface    rgbaColor
		minimum    float64
	}{
		{"text on canvas", tokens.Text, tokens.Canvas, canvas, contrastMinimumText},
		{"text on card", tokens.Text, tokens.Card, card, contrastMinimumText},
		{"text on input", tokens.Text, tokens.Input, input, contrastMinimumText},
		{"text on sidebar", tokens.Text, tokens.Sidebar, sidebar, contrastMinimumText},
		{"muted on canvas", tokens.Muted, tokens.Canvas, canvas, contrastMinimumMuted},
		{"muted on card", tokens.Muted, tokens.Card, card, contrastMinimumMuted},
		{"primary on canvas", tokens.Primary, tokens.Canvas, canvas, contrastMinimumAccent},
	}
	warnings := make([]ContrastWarning, 0, len(checks))
	for _, check := range checks {
		foreground := compositeColor(parseHexRGBA(check.foreground), check.surface)
		ratio := math.Round(colorContrast(foreground, check.surface)*100) / 100
		if ratio < check.minimum {
			warnings = append(warnings, ContrastWarning{
				Pair:       prefix + check.pair,
				Foreground: check.foreground,
				Background: check.background,
				Ratio:      ratio,
				Minimum:    check.minimum,
			})
		}
	}
	return warnings
}
