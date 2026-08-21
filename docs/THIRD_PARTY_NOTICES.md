# Third-Party Notices

This file is an initial development aid for Autoto's direct Go dependencies. It is not legal advice and is not a complete transitive dependency notice.

Before formal distribution, regenerate a complete notice using a license scanner such as `go-licenses` and review the results.

## Direct dependencies

| Dependency | License | Notes |
| --- | --- | --- |
| `github.com/go-chi/chi/v5` | MIT | HTTP router |
| `github.com/google/uuid` | BSD-3-Clause | UUID generation |
| `modernc.org/sqlite` | BSD-3-Clause | Pure-Go SQLite driver |
| `github.com/coder/websocket` | ISC | WebSocket implementation |
| `github.com/openai/openai-go/v3` | Apache-2.0 | OpenAI official Go SDK |
| `github.com/anthropics/anthropic-sdk-go` | MIT | Anthropic official Go SDK |
| `github.com/creack/pty` | MIT | PTY support |

## Suggested verification

```bash
go run github.com/google/go-licenses@latest report ./cmd/autoto
go run github.com/google/go-licenses@latest check ./cmd/autoto
```

The runtime `/api/licenses` endpoint is also a best-effort development helper. It should not replace a reviewed third-party notice for release artifacts.
