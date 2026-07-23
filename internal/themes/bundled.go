package themes

import (
	"embed"
	"fmt"
)

//go:embed assets/argentina-spain-final
var bundledThemeAssets embed.FS

func mustBundledThemeAsset(resourcePath string) []byte {
	data, err := bundledThemeAssets.ReadFile("assets/argentina-spain-final/" + resourcePath)
	if err != nil {
		panic(fmt.Sprintf("read bundled theme asset %s: %v", resourcePath, err))
	}
	return data
}

// builtInThemes returns original Autoto-owned manifests and artwork. Bundled
// assets are local, revisioned, and pass through the same validation path as
// imported theme resources; no third-party or remote artwork is used.
func builtInThemes() map[string]bundledTheme {
	position := func(value int) *int { return &value }
	icons := map[string]string{
		"brand":                "icons/brand.png",
		"rail-home":            "icons/rail-home.png",
		"rail-conversation":    "icons/rail-conversation.png",
		"rail-schedules":       "icons/rail-schedules.png",
		"rail-settings":        "icons/rail-settings.png",
		"rail-collapse":        "icons/rail-collapse.png",
		"sidebar-search":       "icons/sidebar-search.png",
		"sidebar-create":       "icons/sidebar-create.png",
		"sidebar-refresh":      "icons/sidebar-refresh.png",
		"sidebar-project":      "icons/sidebar-project.png",
		"sidebar-conversation": "icons/sidebar-conversation.png",
		"sidebar-collapse":     "icons/sidebar-collapse.png",
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersionV2,
		ID:            "argentina-spain-final",
		Name:          "Argentina–Spain Final",
		Version:       "2.0.0",
		Description:   "A complete original blue-white, gold-red, and black-gold match-night theme with preview artwork, backgrounds, and custom navigation icons.",
		Author:        "Autoto",
		ColorScheme:   ColorSchemeDark,
		Tokens: Tokens{
			Canvas: "#07111F", Sidebar: "#0A1C30", Card: "#10263F",
			Input: "#163552", Text: "#F7FBFF", Muted: "#9DB1C8",
			Border: "#75AADB", Primary: "#75AADB", Secondary: "#F1BF00",
			Danger: "#AA151B", Terminal: "#090A0C", Message: "#132B47",
		},
		Materials: Materials{
			Canvas:   Material{Kind: MaterialSolid, Opacity: 1, Blur: 0, Radius: 0, Shadow: ShadowNone},
			Sidebar:  Material{Kind: MaterialGlass, Opacity: 0.9, Blur: 18, Radius: 0, Shadow: ShadowMedium},
			Card:     Material{Kind: MaterialTranslucent, Opacity: 0.94, Blur: 10, Radius: 18, Shadow: ShadowStrong},
			Input:    Material{Kind: MaterialTranslucent, Opacity: 0.96, Blur: 8, Radius: 14, Shadow: ShadowSoft},
			Terminal: Material{Kind: MaterialSolid, Opacity: 1, Blur: 0, Radius: 14, Shadow: ShadowStrong},
			Message:  Material{Kind: MaterialGlass, Opacity: 0.92, Blur: 12, Radius: 18, Shadow: ShadowMedium},
		},
		Preview: "preview.png",
		Backgrounds: &Backgrounds{
			Global: &BackgroundAsset{Path: "backgrounds/global.png", PositionX: position(50), PositionY: position(50), FallbackOpacity: 0.2},
			Home:   &BackgroundAsset{Path: "backgrounds/home.png", PositionX: position(50), PositionY: position(50), FallbackOpacity: 0.28},
		},
		Icons: icons,
	}
	resources := map[string][]byte{
		manifest.Preview:                 mustBundledThemeAsset(manifest.Preview),
		manifest.Backgrounds.Global.Path: mustBundledThemeAsset(manifest.Backgrounds.Global.Path),
		manifest.Backgrounds.Home.Path:   mustBundledThemeAsset(manifest.Backgrounds.Home.Path),
	}
	for _, resourcePath := range icons {
		resources[resourcePath] = mustBundledThemeAsset(resourcePath)
	}
	return map[string]bundledTheme{
		manifest.ID: {manifest: manifest, resources: resources},
	}
}
