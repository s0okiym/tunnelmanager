# AGENTS.md

Guidance for AI coding agents working on this repository. Everything below is grounded in the actual source, not the roadmap. Where `DESIGN.md` / `README.md` describe features that are **not** implemented, this file flags them explicitly so you don't trust stale prose — treat this file as the source of truth for what the code really does.

## Project overview

`tunnel` is a single-binary, port-forwarding tool written in Go, roughly an `ssh -L/-R/-D` replacement with its own lightweight framed control protocol. Three forwarding modes:

- **local** (`-L [bind:]port:host:hostport`) — listen locally, dial a target, relay bytes. Like `ssh -L`.
- **remote** (`-R [bind:]port:host:hostport -s server:port` + a server side `-s addr`) — NAT penetration: the client dials out to a public server over a persistent control connection; the server accepts public connections on `[bind:]<port>` and multiplexes them back over that control connection to the client's local target. Like `ssh -R`.
- **dynamic** (`-D [bind:]port`) — a SOCKS5 (CONNECT only) proxy. Like `ssh -D`.

It runs in two ways: **ad-hoc** (foreground, zero config, driven by `-L/-R/-D/-s` flags) and **daemon** (`tunnel start`, managed via a YAML config + a Unix-socket control channel). No external network dependencies; only the Go stdlib plus `gopkg.in/yaml.v3` for config parsing.

## Build and test commands

- **Build all:** `go build ./...`
- **Build binary:** `go build -o tunnel ./main.go` (output `tunnel` is git-ignored)
- **Test (all):** `go test -race -count=1 ./...`
- **Test one package:** `go test -race -count=1 ./relay/` (or `./manager/`, `./e2e/`)
- **Single test:** `go test -race -count=1 -run TestProxyEcho ./relay/`
- **Benchmark:** `go test -run x -bench BenchmarkProxyThroughput ./relay/`
- **Vet / typecheck:** `go vet ./...`
- **Lint:** none configured (no linter in the repo).

All of the above currently pass clean: `go build ./...` and `go vet ./...` produce no output, and `go test -race -count=1 ./...` reports `ok` for all four packages (`tunnel` root, `tunnel/e2e`, `tunnel/manager`, `tunnel/relay`). Always run tests with `-race` — the relay and manager code is heavily concurrent and the race detector is the primary safety net. The `e2e` package includes a process-level test (`TestE2EDaemonLifecycle`) that `go build`s the binary and drives the real daemon over its control socket, so the first `e2e` run compiles the CLI.

## Tech stack

- **Language:** Go — `go.mod` declares `go 1.26.2` (built/tested with go1.26.2). Note this is far newer than the "Go 1.22+" line in `DESIGN.md`.
- **TLS:** `crypto/tls`, pinned to TLS 1.3 (`MinVersion: tls.VersionTLS13`).
- **Certs:** `crypto/ecdsa` P-256 self-signed, generated **in memory** at startup (see caveats).
- **Config:** `gopkg.in/yaml.v3 v3.0.1` (the only third-party dep; listed as `// indirect` in `go.mod` but used directly by `manager/config.go`).
- **CLI:** stdlib `flag` (manual `os.Args[1]` subcommand dispatch in `main.go`).
- **Daemon control:** Unix domain socket + `encoding/json` (one request/response per connection, not persistent JSON-RPC).
- **Concurrency:** goroutine-per-connection; `sync.Pool` 32 KB buffers; `io.CopyBuffer` (uses `splice(2)` automatically for `*net.TCPConn` on Linux).

## Project structure

```
tunnel/
├── main.go                 — CLI entrypoint: subcommand dispatch, flag parsing,
│                             ad-hoc runners, daemon runner, signal handling,
│                             -L/-R spec parsers, buildTunnelConfig, applyGlobalConfig
├── main_test.go            — buildTunnelConfig (add-flag) tests
├── manager/                — daemon side (package manager)
│   ├── config.go           — Config/TunnelConfig/GlobalConfig YAML types, Load/Save
│   │                         (Load honors $TUNNEL_CONFIG), dir resolution
│   ├── manager.go          — Manager: per-tunnel goroutine lifecycle (start/stop/
│   │                         add/remove/list/groups/enable/disable/restart/save/Reload)
│   │                         + HandleControl dispatch
│   ├── control.go          — Unix-socket JSON control server/client (stoppable
│   │                         ServeControl, 0600 socket, read timeout), socket-path override
│   ├── daemon.go           — pidfile write/read/remove, IsRunning (kill -0 probe)
│   ├── logging.go          — SetupLogFile (redirect daemon log to a file)
│   ├── systemd.go          — systemd unit generation (system + user; user path MkdirAll'd)
│   ├── manager_test.go     — config round-trip, add/remove/list, enable/disable/restart/save, HandleControl, pidfile
│   ├── lifecycle_test.go   — reload rebinds port, stop releases port, concurrent reload (race)
│   ├── control_test.go     — control transport round-trip, socket perms, read timeout
│   └── completeness_test.go— TUNNEL_CONFIG env, systemd-user MkdirAll, socket override, log file
├── relay/                  — forwarding core (package relay)
│   ├── relay.go            — Relay(a,b): bidirectional io.CopyBuffer + buffer pool +
│   │                         half-close (closeWrite); Stats
│   ├── tcpproxy.go         — Proxy: local -L listener (plain + NewTLSProxy)
│   ├── socks5.go           — SOCKS5 handshake parsing (RFC 1928, CONNECT only)
│   ├── socks5proxy.go      — SocksProxy: -D dynamic proxy
│   ├── chainproxy.go       — ChainProxy: multi-hop entrypoint (Addr(); see caveats)
│   ├── ctrlframe.go        — binary Frame protocol: Read/WriteFrame + frame type consts
│   ├── stream.go           — channel: net.Conn over the mux; Close removes it from the map
│   ├── ctrlconn.go         — CtrlConn: mux read loop, channel map (removeChannel),
│   │                         OpenChannel/AcceptChannel, LastActivity, KeepAlive
│   ├── auth.go             — Auth{Client,Server} token handshake, Register{Client,Server}
│   ├── reconnect.go        — BackoffDelay: exponential backoff + jitter
│   ├── tls.go              — TLSConfig, SetupTLS, GenerateCert, TLSListener/TLSDial
│   ├── healthcheck.go      — HealthCheck: periodic TCP dial, logs down/recovered
│   ├── remoteserver.go     — RemoteServer (-s): accept ctrl conns, auth, register, open
│   │                         remote listeners (shared per port, round-robin across ctrl conns)
│   ├── remoteclient.go     — RemoteClient (-R): dial+auth+register, reconnect loop,
│   │                         accept channels, relay to local target
│   ├── relay_test.go       — unit tests (proxy, relay, SOCKS5, frame, ctrlconn, TLS, backoff)
│   ├── channel_cleanup_test.go — channel map does not leak after Close
│   ├── keepalive_test.go   — dead-peer detection + healthy-peer keepalive
│   └── robustness_test.go  — half-close reverse dir, malformed frames, chainproxy, healthcheck
├── e2e/
│   ├── e2e_test.go         — end-to-end tests over loopback (local/socks/remote/TLS/auth)
│   ├── reconnect_test.go   — remote reconnect resumes; server frees port on disconnect; no shutdown hang
│   ├── coverage_test.go    — remote target-unreachable; TLS+token+remote combined
│   └── daemon_test.go      — process-level: build binary, start/ls/forward/SIGHUP-reload/enable/disable/restart/save/stop
├── DESIGN.md               — original design doc (Chinese; aspirational — see caveats)
├── README.md               — user-facing usage guide (Chinese; also partly aspirational)
├── go.mod / go.sum
└── AGENTS.md
```

## Runtime architecture

### Local mode (`Proxy`, `tcpproxy.go`)
`net.Listen` → per-connection goroutine → `net.Dial` the target → `Relay`. `NewTLSProxy` wraps the listener with `tls.Listen`; the encrypted segment is only client↔tunnel, the tunnel→target hop is plain TCP.

### Dynamic mode (`SocksProxy`, `socks5proxy.go` + `socks5.go`)
`net.Listen` → SOCKS5 greeting (no-auth only) → CONNECT request → parse target (IPv4/IPv6/domain) → `net.Dial` → reply with bound address → `Relay`. Only the CONNECT command is supported.

### Remote mode (the interesting part)
- **Control connection** is a single TCP (or TLS) stream carrying length-prefixed frames.
- **Frame format** (`ctrlframe.go`): `Len(4B big-endian) | Type(1B) | Payload`. Types: `Ping 0x01`, `Pong 0x02`, `NewChannel 0x03`, `ChannelData 0x04`, `ChannelClose 0x05`, `AuthRequest 0x06`, `AuthResponse 0x07`, `Register 0x08`. (Note: these opcodes differ from the numbering sketched in `DESIGN.md`; trust `ctrlframe.go`.)
- **Handshake order (before the multiplexer takes over):** optional `AuthClient`/`AuthServer` token exchange → `RegisterClient`/`RegisterServer` (client tells server which `RemotePort → TargetAddr` tunnels to open). These are standalone functions in `auth.go`; only *after* they complete does `NewCtrlConn` start the read loop.
- **Multiplexing (`CtrlConn` + `channel`):** each accepted public connection on the server opens a new logical `channel` (a `net.Conn` backed by the shared control connection). `ChannelData` frames carry `channel_id(4B) + data`. Writes to the underlying conn are serialized by `wmu`; the read loop dispatches frames to per-channel buffers guarded by a `sync.Cond`. `channel.Close()` removes itself from the map (`removeChannel`) so the map does not leak per connection.
- **Keepalive & dead-peer detection:** client sends `Ping` every 15s (`KeepAlive`); server echoes `Pong`. `CtrlConn` timestamps the last inbound frame (`LastActivity`); if `KeepAlive` sees no frame within the timeout window (45s) it closes the control connection so the reconnect loop takes over. `FramePong` needs no explicit handler beyond that timestamp (refreshed generically in the read loop).
- **Reconnect:** `RemoteClient.Run` loops forever, using `BackoffDelay` (base 1s, cap 30s, 20% jitter, ≤30 doublings, infinite attempts). On the **server** side, each remote port has a shared public listener whose lifetime spans the registered control connections (see key gotchas), so a client reconnecting re-binds the same remote port cleanly.

### Daemon mode
`tunnel start` writes a pidfile, builds a `Manager`, autostarts tunnels with `autostart: true`, and serves a Unix-socket control channel. Each managed tunnel runs in its own goroutine cancelled via a per-tunnel `close(ctx)`; `stopLocked` cancels every tunnel and then **waits for each goroutine to fully return** (releasing its listener) before proceeding, so reloads don't race the old listener's close. `SIGHUP` calls `Manager.Reload()` **in place** (same `*Manager`, under `m.mu`) so the bound control-server handler keeps working after a reload. `SIGINT`/`SIGTERM` and a `stop` control request both drive a graceful shutdown of the whole daemon (via a shutdown channel; `stop` defers the shutdown briefly so its response is flushed to the client first). CLI subcommands (`ls`, `add`, `rm`, `reload`, `enable`, `disable`, `restart`, `save`, `start-group`, `stop-group`, `stop`) are thin clients that dial the socket and send one JSON request.

## Configuration

- Config file: `~/.config/tunnel/config.yaml` (falls back to `/etc/tunnel/config.yaml` when `$HOME` is unset).
- Data dir: `~/.local/share/tunnel/` (or `/var/lib/tunnel`) holds `tunneld.pid` and `control.sock`.
- Config types live in `manager/config.go`. `TunnelConfig` fields: `name`, `mode`, `local`, `remote`, `server`, `token`, `tls` (bool), `tls_cert`, `tls_key`, `tls_verify`, `protocol`, `autostart`, `group`, `hops`, `connections`, `health_check`.
- `GlobalConfig` has two **wired** fields: `log_file` (daemon redirects its log there via `manager.SetupLogFile`) and `control_socket` (overrides the control socket path via `manager.SetControlSocketPath`; `applyGlobalConfig` in `main.go` applies it for both the daemon and client commands so they agree on the path). The previously-decorative `log_level` and `tls_dir` fields were removed.
- `LoadConfig("")` resolves the path from `$TUNNEL_CONFIG` first, then `~/.config/tunnel/config.yaml`. A missing config file is **not** an error — `LoadConfig` returns `DefaultConfig()` (empty).

## Implemented vs. designed-but-not

`DESIGN.md` and `README.md` are aspirational in places. What actually exists:

**Implemented:** local/remote/dynamic forwarding; TLS 1.3 (`--tls`, auto self-signed or `--tls-cert`/`--tls-key`, `--tls-verify`, `--server-fingerprint`, `--trust-on-first-use`); persistent auto-generated TLS identity stored in `~/.config/tunnel/identity/` with **automatic certificate rotation 30 days before expiry** (same key pair, public-key fingerprint stays stable); token auth; binary frame protocol + multiplexer with per-connection channel cleanup; half-close; auto-reconnect with backoff+jitter (server frees and re-binds the remote port across reconnects); dead-peer detection on the control connection; daemon (`start [--background]`, `stop`, `ls`, `add`, `rm`, `reload`, `enable`, `disable`, `restart`, `save`, `status`, `logs`, `start-group`, `stop-group`); in-place `SIGHUP` reload; signal handling; `init --systemd` / `--systemd-user`; health check (log-only); `$TUNNEL_CONFIG` env; `log_file`/`control_socket` global config; control socket restricted to `0600` with an idle read timeout; `tunnel add` for all of `-L`/`-R`/`-D` (`-R` requires `-s` and also captures `--token`/`--tls`/`--tls-verify`/`--tls-cert`/`--tls-key`/`--server-fingerprint`/`--trust-on-first-use`/`--group`/`--autostart`); config-file multi-hop and multi-connection fields; `connections: N` load balancing for remote forwarding (shared listener with round-robin distribution across control connections); configurable remote listener bind address (`-R [bind:]port:host:hostport`); richer `tunnel ls` status (`starting`/`listening`/`established`/`reconnecting`/`degraded`/`error`/`stopped`, plus `since` and `reconnect_count`); per-channel flow control (`FrameWindowUpdate`) with bounded receive buffers; TCP and UDP local forwarding (`--udp`).

**Present as a flag/field but NOT functional:**
- `metrics` / `expvar` / Prometheus, and `tunnel logs` / `status` per-tunnel metrics, and `--project` — described in docs but **not** wired into `main.go`'s subcommand switch. (Basic `tunnel status` and `tunnel logs` are implemented; they show runtime status and recent log lines, not Prometheus metrics.)
- **Multi-hop (`hops`)** — `ChainProxy` only dials `hops[0]` and relays; it does not chain through subsequent hops itself (each hop must be its own running tunnel).
- `tunnel ls` does not show per-tunnel active connection counts; it shows status (`starting`/`listening`/`established`/`reconnecting`/`degraded`/`error`/`stopped`), `since`, and `reconnect_count`.
- The multiplexer has **per-channel flow control**: each logical channel has a send window (default 64 KB) replenished by `FrameWindowUpdate` as the receiver consumes data. Writes larger than the available window are fragmented and block, back-pressuring only that channel instead of all traffic on the control connection. A 256 KB safety cap on the per-channel receive buffer prevents unbounded growth if the peer ignores the window.

## Code style and conventions

- Standard Go: tabs, `gofmt` layout, package-per-directory. Run `gofmt`/`go vet` before finishing; keep `go vet ./...` clean.
- Exported identifiers use Go doc-comment style; comments are written in **English** even though `README.md`/`DESIGN.md` prose is Chinese. Match the surrounding language when editing.
- Errors are wrapped with `fmt.Errorf("context: %w", err)`; keep that pattern.
- Logging uses the stdlib `log` package with a `component:` prefix (`remote:`, `manager:`, `health:`, `chain:`).
- No dependency additions without strong reason — the "stdlib + yaml only" property is a deliberate design goal.
- Keep changes minimal and local; follow existing patterns (e.g. the `select { case <-stop: proxy.Close() case <-errCh: }` shutdown idiom in `manager.go`).

## Testing instructions

- Four test packages: `tunnel` root (`main_test.go`, `buildTunnelConfig`), `relay` (unit), `manager` (config/control/pidfile/lifecycle), `e2e` (black-box over loopback + a process-level daemon test).
- **Always use real TCP over loopback (`127.0.0.1:0`) in tests.** `io.Pipe`/`net.Pipe` is unsuitable for bidirectional relay tests — synchronous writes deadlock (though `net.Pipe` is fine for single-frame ctrlconn exchanges). Existing helpers: `startEcho`/`startEchoLoop`, `socks5Connect`, `echoThrough`/`waitEcho`, `findFreePort`.
- **Always run with `-race`** and `-count=1` (disable the test cache) — concurrency correctness is the main thing under test.
- Timing-sensitive tests (reconnect, teardown) synchronize by polling (`waitEcho`, `waitPortFree`, `waitFile`) rather than fixed sleeps where practical; when adding such tests, prefer poll-with-deadline over `time.Sleep`.
- Tests that mutate package globals (`DefaultDataDir`, `controlReadTimeout`, control-socket override) must restore them and, if a server goroutine reads the global, join that goroutine first (see `startControl` in `control_test.go`) to avoid `-race` reports.
- When adding a forwarding feature, add both a `relay` unit test and, where it involves a full path (client+server, TLS, auth), an `e2e` test mirroring the existing `TestE2E*` structure.
- TLS tests generate ephemeral in-memory certs; don't rely on any on-disk cert state. Custom-cert tests should create their own temporary PEM files (e.g. via `t.TempDir()`) rather than depending on fixed paths like `/tmp`.

## Security considerations

- **Encryption:** TLS 1.3 only, no downgrade. Client side defaults to `InsecureSkipVerify = true` (self-signed); passing `--tls-verify` (or `tls_verify: true`) flips it to verify the peer cert against system roots — use it with real `--tls-cert`/`--tls-key`, otherwise verification fails.
- **Auth:** token is a shared secret compared for equality in `AuthServer` (plain `==`, not constant-time; the token is not used for encryption — TLS handles that). Token only gates the control connection.
- **Control socket:** created `0600` (owner-only) and given an idle read timeout, but the control protocol itself is still unauthenticated — anyone who can open the socket can manage tunnels. Keep the socket in a private data dir.
- **Persistent identity / fingerprint pinning:** when `--tls` is used without `--tls-cert`/`--tls-key`, the auto-generated identity is persisted to `~/.config/tunnel/identity/` and the certificate is automatically rotated 30 days before expiry while keeping the same key pair, so the public-key fingerprint stays stable across rotations. Remote clients can pin a server with `--server-fingerprint SHA256:...` or use `--trust-on-first-use` to record it in `~/.config/tunnel/known_hosts`. This gives MITM protection without a CA. mTLS is not implemented.
- **Trust boundary:** the tunnel→target hop (and, for `-L --tls`, tunnel→target) is always plain TCP; only the client↔tunnel / client↔server hop is encrypted. Don't assume end-to-end encryption to the final target.
- Client/server must be built from the **same version**: the frame protocol has no version negotiation and can change between commits.

## Key gotchas

- **Half-close relay:** `Relay` runs two goroutines; each calls `closeWrite` (TCP/TLS `CloseWrite`, or `channel.CloseWrite` which sends a `ChannelClose` frame) on completion, leaving the opposite direction open. Don't "simplify" this to a full `Close` — it breaks request/response protocols.
- **Per-tunnel listener lifetime is tied to the registered control connections:** each `serveRemoteTunnel` attaches its control connection to a shared listener for the remote port. The shared listener closes only when the last registered control connection for that port detaches (or the server is closed). `listenTunnel` retries the bind briefly to cover the reconnect handoff. Don't reintroduce a listener that outlives its registered control connections.
- **Channel map cleanup:** `channel.Close()` calls `cc.removeChannel(id)` so the multiplexer map doesn't grow per handled connection. `CloseWrite` (half-close) must **not** remove the channel — only a full `Close` does.
- **Manager teardown is synchronous:** `stopLocked`/`stopTunnelLocked` wait on each tunnel's `done` channel (5s cap). Reload/stop rely on this to avoid `address already in use`. Any new `run*` mode goroutine must close its `done` (it's wired via the `defer close(done)` wrapper in `startOne`).
- **`SIGHUP` reloads in place:** `Manager.Reload()` mutates the existing manager under `m.mu`; don't swap the `*Manager` out from under `ServeControl`.
- **Buffer pool:** 32 KB buffers via `sync.Pool`, one per direction. Get/Put around each `io.CopyBuffer`.
- **`splice(2)`:** `io.CopyBuffer` on `*net.TCPConn` auto-uses `splice(2)` on Linux; no explicit syscall. The supplied buffer is bypassed in that path — that's expected.
- **Handshake before read loop:** for remote mode, `AuthClient/Server` and `RegisterClient/Server` read/write frames *directly* on the raw conn. Only afterwards does `NewCtrlConn` start the read loop and take over frame dispatch. Doing auth/register after starting `CtrlConn` will race the read loop.
- **`channel` is a full `net.Conn`** but `LocalAddr`/`RemoteAddr` return `nil` and deadline setters are no-ops. Code relaying over channels must not depend on those.
- **`remoteserver.go` listens on the configured bind address** (`0.0.0.0` by default) for each registered tunnel. The remote spec supports `[bind:]port:host:hostport` for both ad-hoc `-R` and daemon config. Multiple control connections registering the same bind:port share a single listener and incoming connections are distributed round-robin; the listener closes only after the last registered control connection for that bind:port detaches.
