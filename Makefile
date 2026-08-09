.PHONY: check check-desktop fmt build-cli build-desktop release-desktop

# The desktop shell must link as a GUI binary on Windows. Without -H windowsgui
# the PE subsystem stays CONSOLE, so the app allocates its own console window:
# a black cmd window sits behind the UI for the whole session and flashes on
# every redraw. CREATE_NO_WINDOW on child processes cannot fix that, because the
# offending console belongs to the desktop binary itself. Empty off Windows,
# where the flag does not apply. Resolved through `go env GOOS` so a cross build
# with GOOS=windows is linked correctly too.
DESKTOP_GUI_LDFLAGS := $(if $(filter windows,$(shell go env GOOS)),-H windowsgui,)

check:
	./scripts/check.sh

# Includes Wails desktop package; needs native WebView toolchain.
check-desktop:
	AUTOTO_CHECK_DESKTOP=1 ./scripts/check.sh

fmt:
	gofmt -w ./cmd ./internal

build-cli:
	go build -o autoto ./cmd/autoto

build-desktop:
	go build -tags desktop -ldflags "$(DESKTOP_GUI_LDFLAGS)" -o autoto-desktop ./cmd/autoto-desktop

# Release-like desktop binary (no Wails debug/devtools).

# VERSION must be semver when set. config.Version is validated as strict semver
# once it reaches a provider as ClientVersion, and a provider that fails that
# check is skipped silently, leaving an app with no usable model. The old
# $${VERSION:-dev} default stamped a non-semver "dev" and did exactly that, so
# an unset VERSION now keeps the compiled-in default instead.
build-desktop-release:
	go build -tags "desktop,production" -ldflags "$(if $(VERSION),-X autoto/internal/config.Version=$(VERSION) ,)$(DESKTOP_GUI_LDFLAGS)" -o autoto-desktop ./cmd/autoto-desktop

# Binaries + SHA256SUMS under dist/. No signing/notarization.
release-desktop:
	./scripts/build-desktop-release.sh