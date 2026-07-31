# Contributing to Autoto

Thanks for your interest in Autoto. The project is an early local-first Go MVP, so small focused changes are preferred.

## Development setup

Run the canonical application entrypoint locally:

```bash
go run ./cmd/autoto
```

Open:

```text
http://localhost:16888
```

The Go module is `autoto`. `cmd/autoto` is retained only as a legacy command shim; use `cmd/autoto` for development, tests, examples, and new automation.

## Project layout

```text
cmd/autoto            Canonical application entrypoint
cmd/autoto        Legacy compatibility command shim
internal/config       Defaults, config migration, and config loading
internal/db           SQLite schema and persistence
internal/agent        Agent runner and event hub
internal/providers    Model provider integrations
internal/tools        Core coding tools
internal/server       HTTP, WebSocket, static UI, backend registry
docs/ARCHITECTURE.md  Contributor guide to request, agent, provider, and tool flow
```

Start with `docs/ARCHITECTURE.md` when changing cross-cutting behavior such as API routing, WebSocket events, provider streaming, tool execution, Git workflow, or persistence.

## Before submitting changes

Run the unified local check:

```bash
make check
```

If `make` is unavailable, run `./scripts/check.sh` directly. The check script verifies Go formatting without rewriting files, runs Go tests/vet/build, checks embedded JavaScript syntax, and runs embedded JavaScript tests. Use `make fmt` to apply Go formatting.

CI runs the same check script and also runs `golangci-lint`; keep warnings fixed before opening a pull request. Tagged releases are packaged by GoReleaser through `.github/workflows/release.yml`.

## Commit hygiene

- Keep commits focused.
- Do not commit local build outputs such as `/autoto`.
- Do not commit `.env`, local databases, config files with credentials, or API keys.
- Update `PROJECT_PLAN.md` or `README.md` when behavior changes.

## Engineering invariants

Apply these rules to new persistence, API, and frontend work:

1. **Recompute derived security state at trusted boundaries.** Clients submit facts; the server derives hashes, scanner verdicts, risk state, and other conclusions. Put invariants in Store logic and SQLite constraints where practical.
2. **Use compare-and-swap for state transitions.** Mutating state machines must use `UPDATE ... WHERE id = ? AND <expected state/version>` and verify `RowsAffected`. Do not implement transitions as read-check-unconditional-write sequences.
3. **Keep transactions self-contained.** Code inside a transaction must use the transaction handle rather than the default database connection. Do not launch unjoined goroutines or publish success externally before commit.
4. **Guard asynchronous UI requests with a sequence.** Discard stale completions. If a feature needs more than two related loading/error booleans, use an explicit status enum such as `idle/loading/ready/stale/error`.
5. **Do not branch on provider names in business logic.** Add a minimal provider capability only when a real behavioral difference requires one; avoid speculative capability matrices.

## Cache review checklist

Before adding or expanding a cache, document and test:

1. What exact source data and derived result are cached?
2. What bounds the entry count and memory or disk usage?
3. What expires entries, and what is the maximum stale duration?
4. Which schema, scanner, model, or algorithm version invalidates existing entries?
5. Which permission, identity, content-hash, or policy change invalidates an entry?
6. Does a cache failure fail open, fail closed, or fall back to authoritative recomputation?
7. Can cached material contain credentials, prompts, private paths, or other secrets, and how is it protected?

A cache must not weaken the authoritative security path. Corrupt or unverifiable security metadata should be recomputed or disabled fail-closed.

## Testing guidance

Prefer tests that do not require external model providers. Use fakes or `httptest.Server` for provider and HTTP behavior where possible.

Useful areas for coverage:

- Config loading, migration, and secret persistence behavior
- SQLite store behavior
- Tool path boundaries and risk classification
- Agent loop behavior with fake providers
- HTTP handler behavior
- Backend registry health checks
