package themes

import (
	"archive/zip"
	"bytes"
	"errors"
	"image/color"
	"strings"
	"testing"
)

func testManifestV3() Manifest {
	manifest := testManifest()
	manifest.SchemaVersion = SchemaVersionV3
	manifest.Preview = ""
	manifest.HomeBackground = nil
	manifest.StatusTokens = &StatusTokens{Success: "#22C55E", Warning: "#F59E0B", Attention: "#EF4444", Info: "#38BDF8"}
	dark := Tokens{
		Canvas: "#0B0F1A", Sidebar: "#101726", Card: "#141C2E", Input: "#1B2438",
		Text: "#EAF1FA", Muted: "#9DB1C8", Border: "#33415C", Primary: "#8FB8E8",
		Secondary: "#F1BF00", Danger: "#F87171", Terminal: "#05070C", Message: "#182338",
	}
	manifest.DarkTokens = &dark
	return manifest
}

func TestV3ManifestValidation(t *testing.T) {
	valid := testManifestV3()
	if err := ValidateManifest(valid); err != nil {
		t.Fatalf("valid v3 manifest rejected: %v", err)
	}
	capabilities := capabilitiesForManifest(valid)
	if !capabilities.StatusTokens || !capabilities.DarkVariant {
		t.Fatalf("v3 capabilities = %#v", capabilities)
	}

	// The new vocabulary must not leak backward: older schema versions have
	// never carried it, so a v1/v2 manifest declaring it is malformed.
	downgraded := valid
	downgraded.SchemaVersion = SchemaVersionV2
	if err := ValidateManifest(downgraded); err == nil || !strings.Contains(err.Error(), "schemaVersion=3") {
		t.Fatalf("v2 manifest with v3 tokens error = %v", err)
	}

	badStatus := testManifestV3()
	badStatus.StatusTokens = &StatusTokens{Success: "url(javascript:alert(1))"}
	if err := ValidateManifest(badStatus); err == nil || !strings.Contains(err.Error(), "statusTokens success") {
		t.Fatalf("invalid status color error = %v", err)
	}

	// A partial dark palette would mix light tokens into dark mode, so the
	// variant is all-or-nothing.
	partialDark := testManifestV3()
	partial := *partialDark.DarkTokens
	partial.Canvas = ""
	partialDark.DarkTokens = &partial
	if err := ValidateManifest(partialDark); err == nil || !strings.Contains(err.Error(), "darkTokens token canvas") {
		t.Fatalf("partial dark palette error = %v", err)
	}

	// Both blocks are optional: v3 without them is just a v2-shaped manifest.
	minimal := testManifestV3()
	minimal.StatusTokens = nil
	minimal.DarkTokens = nil
	if err := ValidateManifest(minimal); err != nil {
		t.Fatalf("minimal v3 manifest rejected: %v", err)
	}
	capabilities = capabilitiesForManifest(minimal)
	if capabilities.StatusTokens || capabilities.DarkVariant {
		t.Fatalf("minimal v3 capabilities = %#v", capabilities)
	}
}

func TestV3CSSEmitsStatusTokensAndDarkVariant(t *testing.T) {
	store := newTestStore(t)
	manifest := testManifestV3()
	result, err := store.InstallManifest(manifest, ImportOptions{})
	if err != nil {
		t.Fatalf("InstallManifest() error = %v", err)
	}
	css, err := store.CSS(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"--autoto-status-success: #22C55E;",
		"--autoto-status-warning: #F59E0B;",
		"--autoto-status-attention: #EF4444;",
		"--autoto-status-info: #38BDF8;",
		`body.white-shell[data-autoto-theme="` + manifest.ID + `"].theme-dark {`,
		"--autoto-color-canvas: #0B0F1A;",
	} {
		if !strings.Contains(css, expected) {
			t.Fatalf("generated CSS is missing %q:\n%s", expected, css)
		}
	}
	if result.Theme.Capabilities.DarkVariant != true {
		t.Fatalf("installed capabilities = %#v", result.Theme.Capabilities)
	}

	// Themes without the new vocabulary still get a complete status palette,
	// derived from their own tokens where that reads best.
	legacy := testManifest()
	legacyCSS, err := GenerateCSS(Theme{Manifest: legacy, Revision: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"--autoto-status-attention: var(--autoto-color-danger);",
		"--autoto-status-info: var(--autoto-color-primary);",
		// testManifest declares a dark scheme, so the stock pair is the
		// lighter one.
		"--autoto-status-success: #4ade80;",
		"--autoto-status-warning: #fbbf24;",
	} {
		if !strings.Contains(legacyCSS, expected) {
			t.Fatalf("legacy CSS is missing %q:\n%s", expected, legacyCSS)
		}
	}
	// A dark-scheme legacy theme restates its status colors for the shell's
	// dark cascade, but it has no alternative palette to emit there.
	darkBlock := legacyCSS[strings.Index(legacyCSS, ".theme-dark {"):]
	if strings.Contains(darkBlock, "--autoto-color-canvas:") {
		t.Fatalf("legacy CSS must not emit a dark palette:\n%s", legacyCSS)
	}
	if !strings.Contains(darkBlock, "--autoto-status-warning: #fbbf24;") {
		t.Fatalf("legacy CSS is missing the dark status restatement:\n%s", legacyCSS)
	}
}

func TestAuditContrastFlagsUnreadablePairs(t *testing.T) {
	// Near-invisible muted text on the canvas, readable everything else.
	manifest := testManifestV3()
	manifest.StatusTokens = nil
	manifest.DarkTokens = nil
	manifest.Tokens = Tokens{
		Canvas: "#FFFFFF", Sidebar: "#F1F5F9", Card: "#FFFFFF", Input: "#F8FAFC",
		Text: "#0F172A", Muted: "#E2E8F0", Border: "#CBD5E1", Primary: "#1D4ED8",
		Secondary: "#7C3AED", Danger: "#B91C1C", Terminal: "#0F172A", Message: "#EFF6FF",
	}
	warnings := AuditContrast(manifest)
	found := false
	for _, warning := range warnings {
		if warning.Pair == "muted on canvas" {
			found = true
			if warning.Ratio >= warning.Minimum || warning.Minimum != contrastMinimumMuted {
				t.Fatalf("muted warning = %#v", warning)
			}
		}
		if warning.Pair == "text on canvas" {
			t.Fatalf("readable pair flagged: %#v", warning)
		}
	}
	if !found {
		t.Fatalf("expected a muted-on-canvas warning, got %#v", warnings)
	}

	// The dark palette is audited as its own surface set with a distinct name,
	// because dark mode renders exactly those tokens.
	dark := manifest
	darkTokens := manifest.Tokens
	darkTokens.Canvas = "#0B0F1A"
	darkTokens.Card = "#141C2E"
	darkTokens.Input = "#1B2438"
	darkTokens.Sidebar = "#101726"
	darkTokens.Text = "#1E293B"
	dark.DarkTokens = &darkTokens
	darkWarnings := AuditContrast(dark)
	foundDark := false
	for _, warning := range darkWarnings {
		if warning.Pair == "dark text on canvas" {
			foundDark = true
		}
	}
	if !foundDark {
		t.Fatalf("expected a dark-variant warning, got %#v", darkWarnings)
	}

	// A palette built for reading produces no warnings at all.
	clean := manifest
	clean.Tokens.Muted = "#475569"
	if warnings := AuditContrast(clean); len(warnings) != 0 {
		t.Fatalf("clean palette warnings = %#v", warnings)
	}
}

func TestInstallManifestUpdateFlowAndLimits(t *testing.T) {
	store := newTestStore(t)
	manifest := testManifestV3()
	first, err := store.InstallManifest(manifest, ImportOptions{})
	if err != nil {
		t.Fatalf("InstallManifest() error = %v", err)
	}
	if first.Replaced || first.PreviousVersion != "" || first.Theme.Source != SourceLocal {
		t.Fatalf("first install result = %#v", first)
	}

	// Same ID without replace: the conflict names the installed version so
	// the UI can ask "replace v1.0.0?" without a second request.
	if _, err := store.InstallManifest(manifest, ImportOptions{}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "installed version 1.0.0") {
		t.Fatalf("conflict error = %v", err)
	}

	updated := manifest
	updated.Version = "1.1.0"
	second, err := store.InstallManifest(updated, ImportOptions{Replace: true})
	if err != nil {
		t.Fatalf("InstallManifest(replace) error = %v", err)
	}
	if !second.Replaced || second.PreviousVersion != "1.0.0" || second.Theme.Version != "1.1.0" {
		t.Fatalf("update result = %#v", second)
	}

	// The manifest-only path cannot smuggle resource declarations that have
	// no bytes behind them.
	withResources := testManifestV3()
	withResources.ID = "with-resources"
	withResources.Preview = "preview.png"
	if _, err := store.InstallManifest(withResources, ImportOptions{}); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("resource-declaring manifest error = %v", err)
	}

	invalid := testManifestV3()
	invalid.Tokens.Canvas = "not-a-color"
	if _, err := store.InstallManifest(invalid, ImportOptions{Replace: true}); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("invalid manifest error = %v", err)
	}
}

func TestExportArchiveRoundTripsRevision(t *testing.T) {
	store := newTestStore(t)
	manifest := testManifestV3()
	installed, err := store.InstallManifest(manifest, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	archive, filename, err := store.ExportArchive(manifest.ID)
	if err != nil {
		t.Fatalf("ExportArchive() error = %v", err)
	}
	if filename != manifest.ID+"-"+manifest.Version+".autoto-theme" {
		t.Fatalf("export filename = %q", filename)
	}
	imported, err := newTestStore(t).Import(bytes.NewReader(archive), ImportOptions{})
	if err != nil {
		t.Fatalf("re-import of export error = %v", err)
	}
	if imported.Theme.Revision != installed.Theme.Revision {
		t.Fatalf("round trip changed the revision: %s != %s", imported.Theme.Revision, installed.Theme.Revision)
	}
}

func TestExportArchiveIncludesResourcesAndLicense(t *testing.T) {
	store := newTestStore(t)
	manifest := testManifest()
	preview := testPNG(t, color.RGBA{R: 0x75, G: 0xaa, B: 0xdb, A: 0xff})
	background := testPNG(t, color.RGBA{R: 0xf1, G: 0xbf, B: 0x00, A: 0xff})
	license := []byte("Original test license\n")
	installed, err := store.Import(bytes.NewReader(themeArchive(t, manifest, preview, background, license)), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	archive, _, err := store.ExportArchive(manifest.ID)
	if err != nil {
		t.Fatalf("ExportArchive() error = %v", err)
	}
	zipReader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(zipReader.File))
	for _, file := range zipReader.File {
		names[file.Name] = true
	}
	for _, expected := range []string{ManifestFilename, LicenseFilename, manifest.Preview, manifest.HomeBackground.Path} {
		if !names[expected] {
			t.Fatalf("export is missing %q: %v", expected, names)
		}
	}
	// The license participates in the revision, so keeping it is what makes
	// the export re-import as the same content.
	reimported, err := newTestStore(t).Import(bytes.NewReader(archive), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if reimported.Theme.Revision != installed.Theme.Revision {
		t.Fatalf("resource round trip changed the revision: %s != %s", reimported.Theme.Revision, installed.Theme.Revision)
	}

	// Bundled themes export too: that is the natural starting point for
	// someone remixing a built-in look.
	bundledArchive, bundledName, err := store.ExportArchive("argentina-spain-final")
	if err != nil {
		t.Fatalf("ExportArchive(bundled) error = %v", err)
	}
	if !strings.HasPrefix(bundledName, "argentina-spain-final-") {
		t.Fatalf("bundled export filename = %q", bundledName)
	}
	if _, err := zip.NewReader(bytes.NewReader(bundledArchive), int64(len(bundledArchive))); err != nil {
		t.Fatalf("bundled export is not a readable ZIP: %v", err)
	}
}
