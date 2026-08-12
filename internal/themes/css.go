package themes

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// GenerateCSS emits only server-owned declarations scoped to one selected
// white-shell theme. Manifest values have already passed strict validation.
func GenerateCSS(theme Theme) (string, error) {
	if err := ValidateManifest(theme.Manifest); err != nil {
		return "", err
	}
	if !validRevision(theme.Revision) {
		return "", fmt.Errorf("theme revision is invalid")
	}
	manifest := theme.Manifest
	var builder strings.Builder
	fmt.Fprintf(&builder, "body.white-shell[data-autoto-theme=\"%s\"] {\n", manifest.ID)
	colors := []struct {
		name  string
		value string
	}{
		{"canvas", manifest.Tokens.Canvas}, {"sidebar", manifest.Tokens.Sidebar},
		{"card", manifest.Tokens.Card}, {"input", manifest.Tokens.Input},
		{"text", manifest.Tokens.Text}, {"muted", manifest.Tokens.Muted},
		{"border", manifest.Tokens.Border}, {"primary", manifest.Tokens.Primary},
		{"secondary", manifest.Tokens.Secondary}, {"danger", manifest.Tokens.Danger},
		{"terminal", manifest.Tokens.Terminal}, {"message", manifest.Tokens.Message},
	}
	for _, color := range colors {
		fmt.Fprintf(&builder, "  --autoto-color-%s: %s;\n", color.name, color.value)
	}
	builder.WriteString("  --ws-bg: var(--autoto-color-canvas);\n")
	builder.WriteString("  --ws-canvas: var(--autoto-color-canvas);\n")
	builder.WriteString("  --ws-sidebar: var(--autoto-color-sidebar);\n")
	builder.WriteString("  --ws-surface: var(--autoto-color-card);\n")
	builder.WriteString("  --ws-card: var(--autoto-color-card);\n")
	builder.WriteString("  --ws-surface-muted: var(--autoto-color-input);\n")
	builder.WriteString("  --ws-input: var(--autoto-color-input);\n")
	builder.WriteString("  --ws-text: var(--autoto-color-text);\n")
	builder.WriteString("  --ws-muted: var(--autoto-color-muted);\n")
	builder.WriteString("  --ws-border: var(--autoto-color-border);\n")
	builder.WriteString("  --ws-primary: var(--autoto-color-primary);\n")
	builder.WriteString("  --ws-primary-strong: var(--autoto-color-secondary);\n")
	builder.WriteString("  --ws-primary-soft: color-mix(in srgb, var(--autoto-color-primary) 16%, transparent);\n")
	builder.WriteString("  --autoto-theme-secondary: var(--autoto-color-secondary);\n")
	builder.WriteString("  --ws-danger: var(--autoto-color-danger);\n")
	builder.WriteString("  --autoto-theme-danger: var(--autoto-color-danger);\n")
	builder.WriteString("  --ws-terminal: var(--autoto-color-terminal);\n")
	builder.WriteString("  --autoto-theme-terminal: var(--autoto-color-terminal);\n")
	builder.WriteString("  --ws-message-user: var(--autoto-color-message);\n")
	builder.WriteString("  --autoto-theme-message-user: var(--autoto-color-message);\n")
	writeStatusTokenCSS(&builder, manifest, false)
	materials := []struct {
		name  string
		value Material
	}{
		{"canvas", manifest.Materials.Canvas}, {"sidebar", manifest.Materials.Sidebar},
		{"card", manifest.Materials.Card}, {"input", manifest.Materials.Input},
		{"terminal", manifest.Materials.Terminal}, {"message", manifest.Materials.Message},
	}
	for _, material := range materials {
		writeMaterialCSS(&builder, material.name, material.value)
	}
	builder.WriteString("  --ws-radius: var(--autoto-material-card-radius);\n")
	builder.WriteString("  --ws-shadow: var(--autoto-material-card-shadow);\n")
	builder.WriteString("  --autoto-theme-surface-opacity: var(--autoto-material-card-opacity);\n")
	builder.WriteString("  --autoto-theme-surface-blur: var(--autoto-material-card-blur);\n")
	builder.WriteString("  --autoto-theme-blur: var(--autoto-material-card-blur);\n")
	builder.WriteString("  --autoto-theme-radius: var(--autoto-material-card-radius);\n")
	accentText, useGradient := safeAccentText(manifest.Tokens.Canvas, manifest.Tokens.Primary, manifest.Tokens.Secondary)
	fmt.Fprintf(&builder, "  --autoto-theme-accent-text: %s;\n", accentText)
	if useGradient {
		builder.WriteString("  --autoto-accent-gradient: linear-gradient(135deg, var(--autoto-color-primary), var(--autoto-color-secondary));\n")
	} else {
		builder.WriteString("  --autoto-accent-gradient: var(--autoto-color-primary);\n")
	}
	builder.WriteString("  --autoto-theme-home-overlay: linear-gradient(145deg, color-mix(in srgb, var(--autoto-color-canvas) 82%, transparent), color-mix(in srgb, var(--autoto-color-danger) 34%, transparent), color-mix(in srgb, var(--autoto-color-terminal) 78%, transparent));\n")
	builder.WriteString("  --autoto-theme-global-image: none;\n")
	builder.WriteString("  --autoto-theme-global-position: 50% 50%;\n")
	builder.WriteString("  --autoto-theme-global-fallback-opacity: 0;\n")
	builder.WriteString("  --autoto-theme-home-position: 50% 50%;\n")
	if manifest.SchemaVersion >= SchemaVersionV2 && manifest.Backgrounds != nil {
		if asset := manifest.Backgrounds.Global; asset != nil {
			x, y := backgroundPosition(asset)
			fmt.Fprintf(&builder, "  --autoto-theme-global-image: url(\"/themes/%s/%s/%s\");\n", manifest.ID, theme.Revision, escapeResourcePath(asset.Path))
			fmt.Fprintf(&builder, "  --autoto-theme-global-position: %d%% %d%%;\n", x, y)
			fmt.Fprintf(&builder, "  --autoto-theme-global-fallback-opacity: %s;\n", strconv.FormatFloat(asset.FallbackOpacity, 'f', -1, 64))
		}
	}
	if manifest.SchemaVersion >= SchemaVersionV2 && manifest.Backgrounds != nil && manifest.Backgrounds.Home != nil {
		asset := manifest.Backgrounds.Home
		x, y := backgroundPosition(asset)
		fmt.Fprintf(&builder, "  --autoto-theme-home-image: url(\"/themes/%s/%s/%s\");\n", manifest.ID, theme.Revision, escapeResourcePath(asset.Path))
		fmt.Fprintf(&builder, "  --autoto-theme-home-position: %d%% %d%%;\n", x, y)
	} else if manifest.HomeBackground != nil {
		fmt.Fprintf(&builder, "  --autoto-theme-home-image: url(\"/themes/%s/%s/%s\");\n", manifest.ID, theme.Revision, escapeResourcePath(manifest.HomeBackground.Path))
		builder.WriteString("  --autoto-home-background: var(--autoto-theme-home-image);\n")
	} else {
		builder.WriteString("  --autoto-theme-home-image: radial-gradient(circle at 18% 16%, var(--autoto-color-primary), transparent 34%), radial-gradient(circle at 82% 18%, var(--autoto-color-secondary), transparent 26%), linear-gradient(145deg, var(--autoto-color-canvas) 0 46%, var(--autoto-color-danger) 72%, var(--autoto-color-terminal) 100%);\n")
	}
	builder.WriteString("  --autoto-home-background: var(--autoto-theme-home-image);\n")
	for _, slot := range AllowedIconSlots {
		fmt.Fprintf(&builder, "  --autoto-icon-%s: none;\n", slot)
		fmt.Fprintf(&builder, "  --autoto-icon-%s-fallback-opacity: 1;\n", slot)
		if resource := manifest.Icons[slot]; resource != "" {
			fmt.Fprintf(&builder, "  --autoto-icon-%s: url(\"/themes/%s/%s/%s\");\n", slot, manifest.ID, theme.Revision, escapeResourcePath(resource))
			fmt.Fprintf(&builder, "  --autoto-icon-%s-fallback-opacity: 0;\n", slot)
		}
	}
	builder.WriteString("}\n")
	// The shell ships its own dark restatement of the status vocabulary at
	// body.white-shell.theme-light.theme-dark specificity, so a theme that can
	// render in dark mode must restate its declarations at equal specificity
	// (this stylesheet loads later, so it wins the tie). The dark variant also
	// restates only the raw palette variables here: every alias above
	// references them through var(), so the whole vocabulary follows.
	if manifest.DarkTokens != nil || manifest.ColorScheme == ColorSchemeDark {
		fmt.Fprintf(&builder, "body.white-shell[data-autoto-theme=\"%s\"].theme-dark {\n", manifest.ID)
		if manifest.DarkTokens != nil {
			darkColors := []struct {
				name  string
				value string
			}{
				{"canvas", manifest.DarkTokens.Canvas}, {"sidebar", manifest.DarkTokens.Sidebar},
				{"card", manifest.DarkTokens.Card}, {"input", manifest.DarkTokens.Input},
				{"text", manifest.DarkTokens.Text}, {"muted", manifest.DarkTokens.Muted},
				{"border", manifest.DarkTokens.Border}, {"primary", manifest.DarkTokens.Primary},
				{"secondary", manifest.DarkTokens.Secondary}, {"danger", manifest.DarkTokens.Danger},
				{"terminal", manifest.DarkTokens.Terminal}, {"message", manifest.DarkTokens.Message},
			}
			for _, color := range darkColors {
				fmt.Fprintf(&builder, "  --autoto-color-%s: %s;\n", color.name, color.value)
			}
		}
		writeStatusTokenCSS(&builder, manifest, true)
		builder.WriteString("}\n")
	}
	return builder.String(), nil
}

// writeStatusTokenCSS emits the status color vocabulary. Unset entries derive
// from the palette (attention from danger, info from primary) or use stock
// values chosen for the mode being rendered, so pre-v3 themes keep working
// status colors.
func writeStatusTokenCSS(builder *strings.Builder, manifest Manifest, dark bool) {
	success, warning := "#16a34a", "#d97706"
	if dark || manifest.ColorScheme == ColorSchemeDark {
		success, warning = "#4ade80", "#fbbf24"
	}
	attention := "var(--autoto-color-danger)"
	info := "var(--autoto-color-primary)"
	if tokens := manifest.StatusTokens; tokens != nil {
		if tokens.Success != "" {
			success = tokens.Success
		}
		if tokens.Warning != "" {
			warning = tokens.Warning
		}
		if tokens.Attention != "" {
			attention = tokens.Attention
		}
		if tokens.Info != "" {
			info = tokens.Info
		}
	}
	fmt.Fprintf(builder, "  --autoto-status-success: %s;\n", success)
	fmt.Fprintf(builder, "  --autoto-status-warning: %s;\n", warning)
	fmt.Fprintf(builder, "  --autoto-status-attention: %s;\n", attention)
	fmt.Fprintf(builder, "  --autoto-status-info: %s;\n", info)
}

func backgroundPosition(asset *BackgroundAsset) (int, int) {
	x, y := 50, 50
	if asset != nil && asset.PositionX != nil {
		x = *asset.PositionX
	}
	if asset != nil && asset.PositionY != nil {
		y = *asset.PositionY
	}
	return x, y
}

func writeMaterialCSS(builder *strings.Builder, name string, material Material) {
	fmt.Fprintf(builder, "  --autoto-material-%s-kind: %s;\n", name, material.Kind)
	fmt.Fprintf(builder, "  --autoto-material-%s-opacity: %s;\n", name, strconv.FormatFloat(material.Opacity, 'f', -1, 64))
	fmt.Fprintf(builder, "  --autoto-material-%s-blur: %dpx;\n", name, material.Blur)
	fmt.Fprintf(builder, "  --autoto-material-%s-radius: %dpx;\n", name, material.Radius)
	fmt.Fprintf(builder, "  --autoto-material-%s-shadow: %s;\n", name, shadowCSS(material.Shadow))
}

func shadowCSS(shadow string) string {
	switch shadow {
	case ShadowSoft:
		return "0 8px 24px rgba(15, 23, 42, 0.08)"
	case ShadowMedium:
		return "0 14px 36px rgba(15, 23, 42, 0.16)"
	case ShadowStrong:
		return "0 22px 56px rgba(0, 0, 0, 0.28)"
	default:
		return "none"
	}
}

func escapeResourcePath(resourcePath string) string {
	components := strings.Split(resourcePath, "/")
	for index := range components {
		components[index] = url.PathEscape(components[index])
	}
	return strings.Join(components, "/")
}

type rgbaColor struct {
	r, g, b, a float64
}

func safeAccentText(canvasHex, primaryHex, secondaryHex string) (string, bool) {
	white := rgbaColor{r: 1, g: 1, b: 1, a: 1}
	black := rgbaColor{a: 1}
	canvas := compositeColor(parseHexRGBA(canvasHex), white)
	primary := compositeColor(parseHexRGBA(primaryHex), canvas)
	secondary := compositeColor(parseHexRGBA(secondaryHex), canvas)
	blackMinimum := math.Min(colorContrast(primary, black), colorContrast(secondary, black))
	whiteMinimum := math.Min(colorContrast(primary, white), colorContrast(secondary, white))
	if blackMinimum >= 4.5 || whiteMinimum >= 4.5 {
		if blackMinimum >= whiteMinimum {
			return "#000000", true
		}
		return "#FFFFFF", true
	}
	if colorContrast(primary, black) >= colorContrast(primary, white) {
		return "#000000", false
	}
	return "#FFFFFF", false
}

func parseHexRGBA(value string) rgbaColor {
	hex := value[1:]
	if len(hex) == 3 || len(hex) == 4 {
		var expanded strings.Builder
		for _, char := range hex {
			expanded.WriteRune(char)
			expanded.WriteRune(char)
		}
		hex = expanded.String()
	}
	if len(hex) == 6 {
		hex += "ff"
	}
	channels := [4]float64{}
	for index := range channels {
		parsed, _ := strconv.ParseUint(hex[index*2:index*2+2], 16, 8)
		channels[index] = float64(parsed) / 255
	}
	return rgbaColor{r: channels[0], g: channels[1], b: channels[2], a: channels[3]}
}

func compositeColor(foreground, background rgbaColor) rgbaColor {
	alpha := foreground.a + background.a*(1-foreground.a)
	if alpha == 0 {
		return rgbaColor{}
	}
	return rgbaColor{
		r: (foreground.r*foreground.a + background.r*background.a*(1-foreground.a)) / alpha,
		g: (foreground.g*foreground.a + background.g*background.a*(1-foreground.a)) / alpha,
		b: (foreground.b*foreground.a + background.b*background.a*(1-foreground.a)) / alpha,
		a: alpha,
	}
}

func colorContrast(first, second rgbaColor) float64 {
	firstLuminance := cssLuminance(first)
	secondLuminance := cssLuminance(second)
	if firstLuminance < secondLuminance {
		firstLuminance, secondLuminance = secondLuminance, firstLuminance
	}
	return (firstLuminance + 0.05) / (secondLuminance + 0.05)
}

func cssLuminance(value rgbaColor) float64 {
	linear := func(channel float64) float64 {
		if channel <= 0.04045 {
			return channel / 12.92
		}
		return math.Pow((channel+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(value.r) + 0.7152*linear(value.g) + 0.0722*linear(value.b)
}
