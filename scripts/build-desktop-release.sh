#!/usr/bin/env bash
# Development/release helper for desktop + CLI binaries.
# Does NOT notarize or Authenticode-sign; see docs/DESKTOP_PACKAGING.md.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"

# config.Version is validated as strict semver once it reaches a provider as
# ClientVersion, and a provider that fails that check is skipped silently: the
# app starts, but no model is ever usable. `git describe` output such as
# "windows-preview-20260722-220-g778feef-dirty" is not semver, so the stamp is
# checked here and only forwarded to -X when it is safe. Callers who need an
# exact stamp should pass a semver VERSION explicitly.
SEMVER_RE='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)([-+][0-9A-Za-z.-]+)?$'
if ! printf '%s' "$VERSION" | grep -Eq "$SEMVER_RE"; then
  echo "warning: VERSION '$VERSION' is not semver; building without a version stamp" >&2
  echo "         pass VERSION=1.2.3 to embed one." >&2
  VERSION=""
fi
OUT="${OUT:-dist}"
mkdir -p "$OUT"
echo "Building Autoto $VERSION → $OUT"

# config.Version is a package-level string var so -X can rewrite it. With no
# usable stamp, leave the compiled-in default alone rather than blanking it.
if [ -n "$VERSION" ]; then
  LDFLAGS="-X autoto/internal/config.Version=${VERSION}"
else
  LDFLAGS=""
fi

# On Windows the desktop shell must link as a GUI binary. Without -H windowsgui
# the PE subsystem stays CONSOLE and the app allocates its own console window,
# which sits behind the UI and flashes on every redraw. Child-process flags such
# as CREATE_NO_WINDOW cannot suppress it: that console belongs to the desktop
# binary itself. The CLI keeps the console subsystem on purpose -- it is a
# terminal program and needs its stdout.
DESKTOP_LDFLAGS="$LDFLAGS"
if [ "$(go env GOOS)" = "windows" ]; then
  DESKTOP_LDFLAGS="$DESKTOP_LDFLAGS -H windowsgui"
fi

echo "→ CLI"
go build -ldflags "$LDFLAGS" -o "$OUT/autoto" ./cmd/autoto

echo "→ desktop (tags=desktop,production)"
# production disables Wails debug/devtools mode for release-like binaries.
go build -tags "desktop,production" -ldflags "$DESKTOP_LDFLAGS" -o "$OUT/autoto-desktop" ./cmd/autoto-desktop

(
  cd "$OUT"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 autoto autoto-desktop > SHA256SUMS
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum autoto autoto-desktop > SHA256SUMS
  fi
)

echo "Done."
echo "  CLI:     $OUT/autoto"
echo "  Desktop: $OUT/autoto-desktop"
echo "  Sums:    $OUT/SHA256SUMS (if available)"
echo "Signing/notarization is intentionally out of band."
