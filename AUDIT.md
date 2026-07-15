# Tunnel Manager — Code Review & Architecture Audit

| Field | Value |
|-------|-------|
| **Date** | 2026-07-15 |
| **Scope** | Full repository (`main.go`, `relay/`, `manager/`, `e2e/`) |
| **Baseline** | `main` @ `ce8c0a3` (plus concurrent agent-rules refresh of `AGENTS.md` if present) |
| **Verification** | `go build ./...`, `go vet ./...`, `go test -race -count=1 ./...` — all pass |
| **Test inventory** | ~20 test files, ~134 `Test*` functions |

This document is a full-repo audit: feature inventory, architecture review, code-review findings, and correctness assessment. It is grounded in the source as of the audit date; treat `AGENTS.md` as the living agent guide, and this file as a point-in-time engineering review.

**Overall verdict:** Core forwarding paths (local / remote / dynamic + TLS + daemon lifecycle) are solid and well tested. The control-plane protocol and daemon lifecycle have several **critical/high** issues. Security defaults are intentionally loose (“ops-owned”), not fail-closed.

---

## 1. Feature inventory

### 1.1 Implementation matrix

| Feature | Design / docs | Code status | Notes |
|---------|---------------|-------------|--------|
| Local TCP (`-L`) | P0 | **Complete** | Half-close + buffer pool |
| Local UDP (`--udp`) | README / partial P2 | **Complete (local only)** | No remote/dynamic UDP |
| Remote TCP (`-R` / `-s`) | P0 | **Complete** | Shared listener + round-robin |
| Dynamic SOCKS5 (`-D`) | P0 | **Complete** | CONNECT only |
| SOCKS5 username/password | P2 | **Complete** | RFC 1929 |
| Auto-reconnect + backoff | P0 | **Complete** | Clean disconnect uses zero backoff (see findings) |
| YAML config / daemon | P0 / P1 | **Complete** | Unix-socket control |
| TLS 1.3 | P1 | **Complete** | Peer verify off by default |
| Persistent identity + cert rotation | README | **Complete** | Rotate 30 days before expiry; fingerprint stable |
| Fingerprint pin / TOFU | README | **Complete** | TOFU silently Accepts on first use |
| Token auth | Docs | **Complete** | Plain `==`; cleartext without TLS |
| Multi control conn `connections:N` | P1 | **Complete** | Capped at 10 |
| Group start/stop | P1 | **Complete** | |
| SIGHUP hot reload | P2 | **Complete** | In-place `Manager.Reload` |
| systemd | P2 | **Mostly complete** | User unit template issues (see findings) |
| Health check | P0 | **Partial** | Log-only; often wrong target |
| Multi-hop `hops` | P1 | **Partial** | Only dials `hops[0]` |
| Per-tunnel metrics / Prometheus | P1 | **Missing** | `status`/`logs` exist; no exporter |
| `--project` / expvar | Older docs | **Missing** | |
| UDP remote | P2 | **Missing** | |
| UNIX socket forward | P2 | **Missing** | |
| IPv6 literals | — | **Rejected** | Explicit error on `[]` |
| mTLS | — | **Missing** | |
| Frame protocol versioning | — | **None** | Client/server must match build |

### 1.2 CLI / subcommands

| Command | Status |
|---------|--------|
| Ad-hoc `-L/-R/-D/-s` + TLS/token/UDP/SOCKS auth | OK |
| `start [--background]` / `stop` / `ls` / `add` / `rm` | OK (stop/pidfile race) |
| `reload` / `enable` / `disable` / `restart` / `save` | OK (failed tunnels stuck in map) |
| `status` / `logs` / `start-group` / `stop-group` | OK (limited accuracy) |
| `init --systemd` / `--systemd-user` | Usable but asymmetric |
| `tunnel add -L --tls` | **TLS flags dropped** (manager supports TLS local; CLI `add` does not persist them) |

### 1.3 Correctness verification (this audit)

```text
go build ./...                          # pass
go vet ./...                            # pass
go test -race -count=1 ./...
  ok  tunnel
  ok  tunnel/e2e
  ok  tunnel/manager
  ok  tunnel/relay
```

Coverage is strong on happy paths: echo, large payloads, concurrency, TLS, auth, reconnect, daemon lifecycle, multi-connection. Gaps are adversarial/failure paths (mux read-loop stall, window-waiter hang, `TUNNEL_CONFIG` save path, failed-tunnel map stuck).

---

## 2. Architecture audit

### 2.1 Layering (sound)

```text
main.go          CLI / signals / ad-hoc and daemon entry
manager/         config, control plane, tunnel lifecycle
relay/           pure forwarding and protocol (no manager import)
e2e/             black-box + real-binary process tests
```

Dependency direction is clean: `main → manager → relay`. The “stdlib + yaml only” constraint is a deliberate and good maintainability choice.

### 2.2 Remote mode data plane (core design)

```text
Public conn ──► RemoteServer.sharedListener (round-robin)
                    │ OpenChannel
                    ▼
              CtrlConn mux (frames)
                    │ AcceptChannel
                    ▼
              RemoteClient ──dial──► local target
                    │
                 Relay (half-close)
```

Shared listener lifetime tied to registered control connections, brief bind retry on reconnect handoff, and multi-conn round-robin are thoughtful and match the reconnect/connections tests.

### 2.3 Frame protocol

| Aspect | Design |
|--------|--------|
| Layout | `Len(4 BE) \| Type(1) \| Payload` |
| Types | Ping…Register + **WindowUpdate `0x09`** |
| Flow control | Per-channel 64 KB send window / 256 KB receive cap |
| Half-close | `CloseWrite` → `FrameChannelClose` (same opcode as full close) |
| Max payload | ~16 MB |

**Architectural gaps:**

1. **No role separation:** the server also handles `FrameNewChannel` into `newCh` but never `AcceptChannel` → DoS surface.
2. **No version field:** protocol evolution requires identical builds.
3. **Register has no ACK:** remote listen failure is invisible to the client.
4. **Half-close and full-close share one opcode:** peer can only `setError(io.EOF)`.

### 2.4 Daemon lifecycle

**Strengths:**

- Per-tunnel `done` channel; `stopLocked` waits for listener release
- In-place `Reload` under `m.mu` without swapping `*Manager` under `ServeControl`
- Control socket `0600` + idle read timeout

**Weaknesses:**

- Pidfile has no exclusive lock; client removes pidfile on `stop` before process exit
- Failed tunnels remain in `m.tunnels`; `enable` / `start-group` treat them as running
- Remote client stop path `Close()`s without fully joining `Run()` / `Wait()`

### 2.5 Trust boundaries

| Boundary | Current behavior |
|----------|------------------|
| Control connection | Optional token; empty token = open reverse-port binder |
| TLS | Default skip peer verify; encryption ≠ authentication |
| Control Unix socket | Mode `0600`, no application auth |
| Tunnel → target hop | Always plain TCP (or UDP) |
| SOCKS | Optional user/pass; non-constant-time compare |

Security model is “operator-owned defaults,” not “secure by default.”

### 2.6 Architecture scores (subjective, 1–5)

| Dimension | Score | Notes |
|-----------|-------|--------|
| Data-plane correctness | 4.0 | Happy path solid; flow-control failure path incomplete |
| Protocol completeness | 3.0 | Missing version, ACK, roles, half-close distinction |
| Daemon engineering | 3.5 | Good reload/stop design; pidfile/config path rough |
| Security defaults | 2.5 | TLS no-verify; optional token; cleartext secret compare |
| Observability | 2.5 | status/logs present; no conn counts/metrics |
| Docs vs code | 3.0 | `AGENTS.md` accurate; README/DESIGN partly aspirational |
| Test quality | 4.0 | Broad + race; weak adversarial coverage |
| Dependencies / maintainability | 4.5 | Small surface, clear packages |

---

## 3. Code review findings

Severity labels: **Critical** / **High** / **Medium** / **Low**.

### 3.1 Critical

#### C1. Mux read loop can block forever on `newCh`

- **Where:** `relay/ctrlconn.go` — `handleFrame` → `cc.newCh <- ch` (buffer 64)
- **Issue:** When the buffer is full, the **entire** `readLoop` stalls: Ping/Pong, data, close, and window updates stop. On the server, `AcceptChannel` is never called, so 64 peer-sent `NewChannel` frames permanently freeze that control connection.
- **Fix:** Never block the read loop on application acceptance; non-blocking send + drop/close; reject reverse-direction `NewChannel` on the server role.

#### C2. Channel writers hang after control death when send window is 0

- **Where:** `relay/ctrlconn.go` `closeAll` + `relay/stream.go` `Write`
- **Issue:** `closeAll` only calls `setError(io.EOF)` (wakes readers). Writers blocked on flow control wait on `wcond` and `status >= chWriteClosed` and are never woken → goroutine leak and stuck `Relay`.
- **Fix:** In `closeAll`, for each channel set `status = chClosed`, set error, broadcast `rcond` and `wcond`, then clear the map (full local `Close` semantics without sending frames).

### 3.2 High

| ID | Issue | Where |
|----|--------|--------|
| H1 | No server-side KeepAlive / dead-peer detection; half-open TCP holds public ports | `remoteserver.handleClient` |
| H2 | Auth/Register have no deadlines; hung handshakes stack goroutines | `auth.go`, `handleClient` |
| H3 | Token and SOCKS password use non-constant-time compare; token cleartext without TLS | `auth.go`, `socks5.go` |
| H4 | Empty `token` makes remote server an open reverse-port binder | `remoteserver` |
| H5 | `--tls` alone does not authenticate the server; TOFU silent Accept | `tls.go` |
| H6 | `handleNewChannel` overwrites channel IDs → hijack / orphan | `ctrlconn.go` |
| H7 | `NewManager(cfg, "")` + `SaveConfig("")` ignore `$TUNNEL_CONFIG` on write | `main.go`, `config.go` |
| H8 | `tunnel stop` removes pidfile from the **client** before daemon exits → dual-start window | `cmdStop`, stop handler |
| H9 | Pidfile has no `O_EXCL` / flock; concurrent `start` races | `daemon.go` |
| H10 | Failed tunnels stay in `m.tunnels`; `enable` / `start-group` cannot recover | `startOne` |
| H11 | `runRemoteClient` reads `m.tunnels` without `m.mu` (map race); `lastErr` not surfaced in status | `manager.go` (~556) |

### 3.3 Medium

| ID | Issue |
|----|--------|
| M1 | Clean disconnect reconnects with **zero backoff** (`connect` returns nil) |
| M2 | TLS / local / SOCKS dials lack timeouts (plain remote dial has 10s) |
| M3 | ~16 MB max frame is a memory DoS vector |
| M4 | Half-close and full-close share one wire opcode |
| M5 | UDP sessions unbounded; `DialUDP` under global session lock |
| M6 | SOCKS: when auth not required, method defaults to no-auth even if client did not offer it |
| M7 | Register has no success/failure ACK |
| M8 | `RemoteClient.Close` not idempotent; `Wait` does not join `Run` |
| M9 | `tunnel add -L --tls` drops TLS fields |
| M10 | No config validation (duplicate names, port normalization, `protocol` case) |
| M11 | Reload does not re-apply `global.log_file` / `control_socket` |
| M12 | Health check dials `tc.Local` (often empty for remote client); log-only |
| M13 | Stop wait times out at 5s then continues → possible EADDRINUSE on reload |
| M14 | `known_hosts` multi-process rewrite has no file lock |

### 3.4 Low / design debt

- `ChainProxy` only dials the first hop; empty hops returns `nil, nil`
- `channel` deadline methods are no-ops
- `logs` filters by **substring** of name (`web` matches `web2`)
- systemd user unit reuses system unit template (includes `User=`)
- Auto identity cert SAN is fixed `127.0.0.1` — incompatible with `--tls-verify`
- Active connections are not drained on Close; no connection counts in `ls`
- `WriteFrame` is two syscalls (header then payload); safe only under `wmu`
- `nextID` can wrap after 2³² channels

---

## 4. Correctness & testing assessment

### 4.1 What works well

- Half-close correctly splits `CloseWrite` vs `Close` (map removal only on full close)
- Shared remote listener lifecycle aligns with reconnect tests
- Flow control is complete on the default path
- Daemon reload holds the lock across stop/start; concurrent reload is tested
- e2e includes real-binary daemon lifecycle, TLS+TOFU, multi-connection
- `-race` as the default test gate is the right safety culture

### 4.2 High-risk paths not covered by tests

1. Injecting `NewChannel` toward the server → read-loop stall  
2. Exhausting send window then killing the control connection → writer hang  
3. `TUNNEL_CONFIG=/tmp/x.yaml` add/save/reload path consistency  
4. Failed bind then `enable` recovery  
5. Concurrent `tunnel start`  
6. Threat model for tokenless public servers (document or negative test)

---

## 5. Recommended fix order (no implementation in this audit)

### P0 — Protocol & correctness

1. Unblock the read loop; reject unexpected `NewChannel` on the server  
2. Make `closeAll` fully terminate channels (status + both conds)  
3. Handshake deadlines + server KeepAlive  
4. Reject duplicate channel IDs  

### P1 — Daemon & config

5. Resolve config path once; pass into `NewManager`; align `SaveConfig` with `LoadConfig`  
6. Pidfile removed only on daemon exit; exclusive start (`O_EXCL` / flock)  
7. On tunnel error exit, remove from map or mark restartable  
8. Pass `*managedTunnel` / state into remote runners; plumb errors into `TunnelState`  

### P2 — Security & ops hardening

9. Constant-time secret compare; warn or refuse token without TLS  
10. Remote TLS client: strong warning or fail-closed without pin/TOFU/verify  
11. Default-require token or bind control listen to loopback  
12. Dial timeouts, smaller max frame, UDP session cap, minimum reconnect delay  

### P3 — Product completeness

13. Persist TLS on `add -L`; config validation and port normalization  
14. Register ACK; distinct half-close vs full-close opcodes  
15. Real multi-hop **or** document downgrade; metrics implement or drop from docs  

---

## 6. Summary

| Category | Conclusion |
|----------|------------|
| **Features** | P0 forwarding + daemon + TLS/identity/TOFU/UDP local/SOCKS auth landed; metrics, real multi-hop, UDP remote still gaps |
| **Architecture** | Clean layering and mature remote mux; protocol lacks roles/ACK/version; security defaults are loose |
| **Code quality** | Happy path readable and consistent; concurrency and lifecycle footguns remain |
| **Correctness** | Current suite all green — main paths trustworthy; C1/C2 and H7–H11 are real defects, not doc noise |

**Bottom line:** Suitable today as a personal/lab `ssh -L/-R/-D` replacement with good main-path correctness. Before exposure on untrusted networks or multi-tenant production, address P0/P1 and tighten security defaults.

---

## Appendix A — Key file map

| Concern | Start here |
|---------|------------|
| CLI / ad-hoc flags | `main.go` |
| Daemon lifecycle | `manager/manager.go`, `manager/control.go` |
| Byte relay + half-close | `relay/relay.go` |
| Local TCP / UDP | `relay/tcpproxy.go`, `relay/udpproxy.go` |
| SOCKS5 | `relay/socks5.go`, `relay/socks5proxy.go` |
| Frame mux + flow control | `relay/ctrlframe.go`, `relay/ctrlconn.go`, `relay/stream.go` |
| Remote server/client | `relay/remoteserver.go`, `relay/remoteclient.go` |
| TLS identity / TOFU | `relay/identity.go`, `relay/knownhosts.go`, `relay/tls.go` |
| Config schema | `manager/config.go` |
| Full-path tests | `e2e/e2e_test.go`, `e2e/daemon_test.go` |

## Appendix B — Related docs

- `AGENTS.md` — living source of truth for agents (implemented vs aspirational)
- `DESIGN.md` — original design (Chinese; partly aspirational)
- `README.md` — user guide (Chinese; partly aspirational)
