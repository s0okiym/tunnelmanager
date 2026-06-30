# AGENTS.md

## Commands

- **Build:** `go build ./...`
- **Test (all):** `go test -race -count=1 ./...`
- **Test (relay):** `go test -race -count=1 ./relay/`
- **Single test:** `go test -race -count=1 -run TestProxyEcho ./relay/`
- **Lint:** (none configured — no linter yet)
- **Typecheck:** `go vet ./...`

## Project structure

```
tunnel/
├── main.go           — CLI: ad-hoc -L mode, stub for start/stop
├── relay/
│   ├── relay.go      — core relay (io.CopyBuffer + buffer pool)
│   ├── tcpproxy.go   — TCP listener → dial → relay glue
│   └── relay_test.go — proxy & relay tests (real TCP, race-tested)
├── DESIGN.md
└── AGENTS.md
```

## Phase 1 delivered

- Local TCP forwarding via `tunnel -L [bind:]port:host:hostport`
- Half-close semantics (CloseWrite) — bidirectional relay without deadlock
- Signal handling (SIGINT/SIGTERM graceful shutdown)
- 8 tests with `-race` clean (echo, large 10MB, concurrent, many writes)

## Next phases (per DESIGN.md)

Phase 2: SOCKS5 dynamic proxy (`-D`)
Phase 3: Remote/NAT-penetration forwarding (`-R`) + TLS
Phase 4: Daemon mode + config management

## Key gotchas

- **Relay uses half-close** (`closeWrite` for TCP) — each goroutine closes only its **write** side when done, leaving the read side open for the opposite direction. This is critical for correct proxy behavior.
- **Tests use real TCP** — io.Pipe is unsuitable for bidirectional relay testing (synchronous writes cause circular deadlocks).
- **Buffer pool** — 32KB per direction, reused via `sync.Pool`.
- **No external dependencies** — only stdlib + `gopkg.in/yaml.v3` (Phase 2+).
