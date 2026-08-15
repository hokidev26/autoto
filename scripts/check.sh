#!/usr/bin/env bash
set -euo pipefail

# Default checks stay free of Wails/native WebView so Linux CI and headless
# developers can validate the browser CLI path. Desktop shell is opt-in:
#   AUTOTO_CHECK_DESKTOP=1 ./scripts/check.sh
#   make check-desktop

printf '==> Checking Go formatting\n'
unformatted="$(gofmt -l ./cmd ./internal)"
if [[ -n "$unformatted" ]]; then
  printf 'The following files need gofmt:\n%s\n' "$unformatted"
  printf 'Run `make fmt` or `gofmt -w ./cmd ./internal` to format them.\n'
  exit 1
fi

printf '==> Checking Go module tidiness\n'
go mod tidy -diff

printf '==> Running Go tests (default tags; desktop package excluded)\n'
go test ./...

printf '==> Running go vet (default tags)\n'
go vet ./...

printf '==> Building Go packages (default tags; CLI path)\n'
go build ./...
# Explicit CLI entrypoint so a future build-tag regression is obvious.
go build -o /dev/null ./cmd/autoto

printf '==> Checking source file size budget\n'
# Guard against source files growing back into unmaintainable giants after the
# structure-split work. Implementation .go/.mjs files stay under 1500 lines; CSS
# (split by cascade section) stays under 6000. Test files, locale-data catalogs,
# and legacy trees are exempt. Files still awaiting their own split are
# grandfathered in size_allowlist — shrink that list as they are split; adding to
# it should be rare and justified. New files over budget, or a split file
# regrowing past budget, fail here.
size_budget_go=1500
size_budget_mjs=1500
size_budget_css=6000
size_allowlist=(
  "internal/db/automation_p2p3.go"
  "internal/db/migrations.go"
  "internal/agent/context_ask.go"
  "internal/server/agent.go"
  "internal/server/provider_config.go"
  "internal/server/static/modules/app-main.mjs"
  "internal/server/static/modules/background-tasks.mjs"
  "internal/server/static/modules/chat-rendering.mjs"
  "internal/server/static/modules/provider-console.mjs"
)
size_violations=""
size_check_one() {
  local file="$1" budget="$2" allowed
  for allowed in "${size_allowlist[@]}"; do
    [[ "$file" == "$allowed" ]] && return 0
  done
  local lines
  lines="$(wc -l < "$file" | tr -d ' ')"
  if (( lines > budget )); then
    size_violations+=$'\n'"  $file: $lines lines (budget $budget)"
  fi
}
while IFS= read -r file; do
  size_check_one "$file" "$size_budget_go"
done < <(find cmd internal -name '*.go' ! -name '*_test.go')
while IFS= read -r file; do
  size_check_one "$file" "$size_budget_mjs"
done < <(find internal/server/static -name '*.mjs' ! -name '*.test.mjs' ! -name 'messages-*.mjs')
while IFS= read -r file; do
  size_check_one "$file" "$size_budget_css"
done < <(find internal/server/static -name '*.css')
if [[ -n "$size_violations" ]]; then
  printf 'The following files exceed the size budget:%s\n' "$size_violations"
  printf 'Split them into cohesive modules, or (only with justification) add to size_allowlist in scripts/check.sh.\n'
  exit 1
fi

./scripts/deadcode.sh

printf '==> Checking embedded JavaScript syntax\n'
node --version
node --check internal/server/static/app.js
for file in internal/server/static/modules/*.mjs; do
  node --check "$file"
done

printf '==> Running embedded JavaScript tests\n'
node --test internal/server/static/modules/*.test.mjs

if [[ "${AUTOTO_CHECK_DESKTOP:-}" == "1" ]]; then
  printf '==> Desktop shell (build tags desktop + production)\n'
  go test -tags desktop ./internal/desktop/ -count=1
  go vet -tags desktop ./internal/desktop/ ./cmd/autoto-desktop
  go build -tags desktop -o /dev/null ./cmd/autoto-desktop
  go build -tags "desktop,production" -o /dev/null ./cmd/autoto-desktop
  printf '==> Desktop checks passed\n'
else
  printf '==> Skipping desktop shell (set AUTOTO_CHECK_DESKTOP=1 to include)\n'
fi

printf '==> All checks passed\n'
