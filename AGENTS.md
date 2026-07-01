# AGENTS.md

## Commands

- **Build:** `go build ./...`
- **Test (all):** `go test -race -count=1 ./...`
- **Test (relay):** `go test -race -count=1 ./relay/`
- **Test (manager):** `go test -race -count=1 ./manager/`
- **Single test:** `go test -race -count=1 -run TestProxyEcho ./relay/`
- **Lint:** (none configured — no linter yet)
- **Typecheck:** `go vet ./...`

## Project structure

```
tunnel/
├── main.go                   — CLI: -L/-R/-D flags, signal handling
├── manager/
│   ├── config.go             — YAML config types, load/save
│   ├── daemon.go             — Daemon lifecycle (pidfile, IsRunning)
│   ├── control.go            — Unix socket JSON-RPC server/client
│   ├── manager.go            — Tunnel lifecycle (start/stop/add/remove/list)
│   └── manager_test.go       — Tests for config, manager, control
├── relay/
│   ├── relay.go              — core Relay (io.CopyBuffer + buffer pool + closeWrite)
│   ├── relay_test.go         — 20+ tests (proxy, relay, SOCKS5, remote, TLS, frame)
│   ├── tcpproxy.go           — TCP Proxy (local -L mode)
│   ├── socks5.go             — SOCKS5 handshake handler
│   ├── socks5proxy.go        — SOCKS5 Proxy (dynamic -D mode)
│   ├── ctrlframe.go          — Binary frame protocol (Ping/Pong/NewChannel/ChannelData/etc.)
│   ├── stream.go             — Channel: net.Conn over multiplexed control connection
│   ├── ctrlconn.go           — CtrlConn: control connection multiplexer
│   ├── auth.go               — Auth handshake + Register frames (standalone functions)
│   ├── reconnect.go          — Exponential backoff + jitter
│   ├── tls.go                — TLS 1.3 helpers (auto-cert generation, dial/listen)
│   ├── remoteserver.go       — Remote server (-s mode): accepts control connections,
│   │                            manages remote listeners, opens channels
│   └── remoteclient.go       — Remote client (-R mode): dials server, auth, register,
│                                accepts channels, relays to local target
├── DESIGN.md
└── AGENTS.md
```

## Phase 1-6 delivered (all E2E regression tests passing)

- **Local TCP forwarding** (`tunnel -L [bind:]port:host:hostport`)
- **SOCKS5 dynamic proxy** (`tunnel -D port`)
- **Remote/NAT-penetration forwarding** (`tunnel -R port:host:hostport -s server:port`)
- **TLS 1.3 encryption** (`--tls` flag, auto-generated ECDSA P-256 cert)
- **Token authentication** (`--token` flag)
- **Binary frame protocol**: Ping/Pong/NewChannel/ChannelData/ChannelClose/Auth/Register
- **Control connection multiplexer**: multiple channels over single TCP/TLS connection
- **Half-close semantics** via channel.CloseWrite() — works with TCP and TLS
- **Auto-reconnect** with exponential backoff + jitter, infinite retry
- **Signal handling** (SIGINT/SIGTERM graceful shutdown for all modes)
- 20+ tests with `-race` clean (echo, 10MB, concurrent, many writes, SOCKS5, remote E2E, TLS, frame protocol, backoff)

## Done (all phases)

All 6 phases from DESIGN.md are implemented.

## Key gotchas

- **Relay uses half-close** (`closeWrite` for TCP/TLS/channel) — each goroutine closes only its **write** side when done, leaving the read side open for the opposite direction.
- **Tests use real TCP** — io.Pipe is unsuitable for bidirectional relay testing (synchronous writes cause circular deadlocks).
- **Buffer pool** — 32KB per direction, reused via `sync.Pool`.
- **CtrlConn auth/register** happens *before* the read loop starts (standalone functions), then CtrlConn takes over frame dispatching.
- **Channel.CloseWrite()** sends a ChannelClose frame — the receiver reads EOF but the channel remains usable in the other direction.
- **splice(2) optimization**: `io.CopyBuffer` on `*net.TCPConn` automatically uses `splice(2)` on Linux (via `TCPConn.ReadFrom`/`WriteTo`) — no explicit syscall needed.
- **No external dependencies** — only stdlib + `gopkg.in/yaml.v3` (Phase 4+).
