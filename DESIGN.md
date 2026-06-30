# Tunnel Manager — Design Document

## 1. 三个转发模式

```
┌────────────────────────────────────────────────────────────────┐
│  mode  │  SSH equiv  │  行为                                   │
│────────│─────────────│─────────────────────────────────────────│
│  local │  ssh -L     │  本地监听 → 连到远端目标                  │
│  remote│  ssh -R     │  远端监听 → 数据经过控制通道 → 本地目标    │
│  dyn   │  ssh -D     │  本地 SOCKS5 代理 → 动态解析目标地址      │
└────────────────────────────────────────────────────────────────┘
```

### 1.1 Local 转发

```
本地监听 :3306 ──── 直连 ────→ db.internal:3306
             ↑                           ↑
         隧道进程                   远端目标（隧道端主动拨号）
```

最简路径：`Listener` + `io.Copy`。类似一根网线直插。

### 1.2 Remote 转发（穿透 NAT 关键）

这是 SSH 做不到（需要 SSH 服务端）或做不好的能力。架构完全不同：

```
             控制连接（长连，客户端→服务端主动建立）
      ┌──────────────────────────────────────────┐
      │                                          │
  本地进程                                         公网服务端
  (NAT 后, 无固定 IP)                             (有公网 IP)
      │                                          │
  本地目标:27017                           监听 :27017
      ▲                                          │
      │    反向通道（复用控制连接的数据流）           ▼
      └──────────────────────────────────────────┘
              外部用户 → 公网IP:27017 → 本地:27017
```

控制连接 = 一条 TCP/TLS 长连，承载两种 frame：
- **心跳**（保活 + NAT 映射维持）
- **反向通道**（`NewChannel` 请求，标识"有新连接来了，请处理"）

当远端 listener 收到新连接时：

```
服务端:
  1. 收到 accept
  2. 通过控制连接发送 NewChannel frame（含目标地址）
  3. 启动 relay goroutine: 新连接 ↔ 控制连接新建的 channel stream

客户端:
  1. 收到 NewChannel frame
  2. 连接到 local_target（如 127.0.0.1:27017）
  3. 启动 relay goroutine: channel stream ↔ local_target
```

### 1.3 Dynamic 转发（SOCKS5 代理）

```
本地监听 :1080 ── 接受 SOCKS5 握手 ── 提取目标地址 ── 拨号 ── relay
```

就是一个标准的 SOCKS5 server，Go 实现约 200 行。不需要预配置 `remote`，客户端自己告诉代理连哪里。

---

## 2. Features

| 层级 | Feature | 说明 |
|------|---------|------|
| P0 | **local** TCP 转发 | `local:8080 → remote:target:80` |
| P0 | **remote** TCP 转发 | 控制连接 + 远端 listener，穿透 NAT |
| P0 | **dynamic** SOCKS5 | 本地 SOCKS5 代理 |
| P0 | 控制连接自动重连 | 指数退避 + jitter，remote 模式依赖此连接 |
| P0 | YAML 配置 | 声明式定义所有模式 |
| P0 | 健康检查 | TCP ping 探测 local endpoint |
| P1 | TLS 模式 | `tls: auto` 自动生成证书，控制连接 + 数据流全加密 |
| P1 | 多跳链式 | local/remote 均支持多跳 |
| P1 | 运行时控制 | `tunnel ls / logs / stop / restart` |
| P1 | 每 tunnel 指标 | 字节、错误、连接数、延迟 |
| P1 | 连接组 | 批量启停 |
| P1 | 多个控制连接 | 一个隧道端可维护多条控制连接做负载均衡 |
| P2 | UDP remote | UDP 版反向转发 |
| P2 | systemd unit | `tunnel init --systemd` |
| P2 | UNIX socket | listener/dial 走 UNIX socket |
| P2 | 热加载 | SIGHUP 重新加载配置 |
| P2 | SOCKS5 auth | 用户名密码认证 |

---

## 3. 架构总览

```
                     tunnel CLI
                     │       ▲
             control │       │ JSON over Unix socket
                     ▼       │
                  ┌──────────────┐
                  │   tunneld    │
                  │  (daemon)    │
                  └──────┬───────┘
                         │
              ┌──────────┼──────────┐
              ▼          ▼          ▼
         Local        Remote      Dynamic
         Manager      Manager     Manager
              │          │          │
         ┌────┴────┐  ┌─┴──┐   ┌───┴────┐
         │TCP Relay│  │Ctrl│   │SOCKS5  │
         │io.Copy  │  │Conn│   │Handler │
         └─────────┘  │Mgr │   └────────┘
                      └────┘
```

### 3.1 组件职责

| 组件 | 职责 |
|------|------|
| Local Manager | 启动 local listener，收到连接后 dial remote target，启动 relay |
| Remote Manager | 建立/维护控制连接（outbound），在服务端侧启动 remote listener |
| Dynamic Manager | 启动 SOCKS5 listener，处理 SOCKS 握手，建立 relay |
| Ctrl Conn Manager | 管理控制连接生命周期（建连、保活、重连、通道复用） |
| Relay | `io.CopyBuffer` 双向搬运，buffer pool，连接跟踪 |

### 3.2 控制连接协议（Remote 模式核心）

纯二进制协议，无外部依赖。帧格式：

```
┌──────────────────────────────────────────┐
│  Len (4B, big-endian)   │  Type (1B)     │
├──────────────────────────────────────────┤
│  Payload (Len 字节)                      │
└──────────────────────────────────────────┘
```

Type:
- `0x01` — Ping / Pong（心跳）
- `0x02` — NewChannel（服务端→客户端：有新连接）  
  Payload: `remote_addr_len(2B) + remote_addr + target_addr_len(2B) + target_addr`
- `0x03` — ChannelData（通道数据流，NewChannel 后续）  
  Payload: `channel_id(4B) + raw_data`
- `0x04` — ChannelClose（通道关闭）  
  Payload: `channel_id(4B) + reason(1B)`

数据流生命周期：

```
服务端                               客户端
  │                                    │
  │──── NewChannel(id=1, target) ─────→│
  │                                    │── dial local_target
  │                                    │
  │──── ChannelData(id=1, data) ──────→│──── data ──→ local_target
  │←─── ChannelData(id=1, data) ──────│←─── data ──│
  │                                    │
  │──── ChannelClose(id=1) ───────────→│── close conn
```

数据帧直接在控制连接上复用（多路复用），不需要额外连接。

---

## 4. 配置

`~/.config/tunnel/config.yaml`:

```yaml
tunnels:
  # local 转发 - 跟 ssh -L 完全一样
  - name: dev-db
    mode: local
    local: "127.0.0.1:3306"
    remote: "db.internal:3306"
    protocol: tcp
    tls: plain
    autostart: true

  # remote 转发 - 客户端无固定 IP，穿透 NAT
  - name: my-service
    mode: remote
    local: "127.0.0.1:8080"       # 本地目标（流量最终到这里）
    remote: "0.0.0.0:9090"        # 服务端监听端口
    server: "public.example.com:9000"  # 服务端地址（控制连接目标）
    protocol: tcp
    tls: auto
    autostart: true
    reconnect:
      max_attempts: 0             # 0 = 无限重连
      base_delay: 1s
      max_delay: 60s

  # dynamic 转发 - SOCKS5 代理
  - name: socks-proxy
    mode: dynamic
    local: "127.0.0.1:1080"
    protocol: tcp
    tls: plain

  # remote 转发 - 多通道负载均衡
  - name: load-balanced
    mode: remote
    local: "127.0.0.1:3000"
    remote: "0.0.0.0:3000"
    server: "public.example.com:9000"
    protocol: tcp
    connections: 3                # 建立 3 条控制连接，轮询分发
    tls: auto

  # 多跳 local
  - name: chain-local
    mode: local
    local: "127.0.0.1:443"
    hops:
      - "hop1.internal:7777"
      - "hop2.internal:7777"
    remote: "target.internal:443"
    protocol: tcp

global:
  log_level: info
  log_file: "~/.local/share/tunnel/tunneld.log"
  control_socket: "/run/user/$UID/tunnel.sock"
  tls_dir: "~/.config/tunnel/tls"
```

### 4.1 模式与字段

| 字段 | local | remote | dynamic |
|------|-------|--------|---------|
| `local` | 本地监听地址 | 本地目标地址 | 本地监听地址 |
| `remote` | 远端目标地址 | 服务端监听地址 | — |
| `server` | — | 控制连接地址 | — |
| `hops` | 可选 | 可选（逐跳转发） | — |

---

## 5. CLI — 两种使用模式

### 5.1 Ad-hoc 模式（最常用，零配置）

跟 `ssh -L/-R/-D` 一样，命令行一把梭：

```bash
# 本地转发
tunnel -L 3306:db.internal:3306

# 反向转发（NAT 穿透）
tunnel -R 9090:localhost:8080 -s public.example.com

# SOCKS5 代理
tunnel -D 1080

# 带跳板
tunnel -L 3306:db.internal:3306 -J jump.example.com

# 组合多个
tunnel -L 3306:db:3306 -L 8080:web:80 -D 1080

# TLS 加密
tunnel -L 3306:db:3306 --tls

# 指定转发协议（默认 TCP）
tunnel -L 3306:db:3306 --udp
```

以 `-L/-R/-D` 开头时，自动进入 ad-hoc 模式：
- **前台运行**，日志直接打 stdout
- 无需配置文件，无需 daemon
- `Ctrl+C` 优雅退出，关闭所有转发
- 临时用，用完即走

### 5.2 Daemon 模式（持久化管理）

```bash
tunnel <command> [flags]

Commands:
  start                      启动 daemon（默认前台，--background 后台）
  stop                       停止 daemon
  restart                    重启 daemon
  status                     daemon 和所有 tunnel 概览
  add     -L ... [--name X]  添加 tunnel（不用手写 YAML）
  rm      <name>             删除 tunnel
  enable/disable <name>      开关 autostart
  ls                         列出所有 tunnel + 状态
  logs    <name> [--tail]    查看日志
  metrics                    导出 Prometheus 指标

# 添加 tunnel 到配置（三行等价）
tunnel add -L 3306:db.internal:3306 --name dev-db --autostart
tunnel add -R 9090:localhost:8080 -s public.example.com --name my-api
tunnel add -D 1080 --name web-proxy

# 从文件批量导入
tunnel add --file tunnels.yaml

# 查看状态
tunnel ls
# NAME       MODE    STATUS      LOCAL          REMOTE
# dev-db     local   LISTENING   :3306           db.internal:3306
# my-api     remote  ESTABLISHED localhost:8080  0.0.0.0:9090
# web-proxy  dynamic LISTENING   :1080           -
```

### 5.3 Ad-hoc ↔ Daemon 互转

```bash
# 正在 ad-hoc 跑着，想把当前配置存下来
# （发送 SIGHUP 或输入 save 命令）
tunnel save my-project
# → 写入 ~/.config/tunnel/projects/my-project.yaml

# 下次直接启动
tunnel start --project my-project
```

### 5.4 CLI 设计原则

| 场景 | 命令 | 心智模型 |
|------|------|----------|
| 临时用一下 | `tunnel -L 3306:db:3306` | 跟 ssh 一样 |
| 长期跑 | `tunnel start --background` | 跟 nginx 一样 |
| 添加/删除 | `tunnel add -L ...` | 跟 git remote 一样 |
| 查看状态 | `tunnel ls` | 跟 docker ps 一样 |

格式规则：
- `-L [bind_address:]port:host:hostport`
- `-R [bind_address:]port:host:hostport`
- `-D [bind_address:]port`
- `-J user@host:port`（跳板）
- `-s server:port`（remote 模式服务端地址）
- `--tls` / `--udp`
- `--name` 不传时自动生成（如 `local-3306`）

Exit codes: 0 ok, 1 config error, 2 runtime error, 3 resource conflict

---

## 6. 传输安全

### 6.1 两层模型

```
┌───────────────────────────────────────────┐
│  认证层（身份验证）                          │
│  token / 双向证书                           │
│  确保连接的双方是预期对象                      │
├───────────────────────────────────────────┤
│  加密层（防窃听/防篡改）                     │
│  TLS 1.3 + AEAD                            │
│  确保数据在公网上不可读                       │
└───────────────────────────────────────────┘
```

两层解耦：加密总是启用（公网默认 `--tls`），认证方式可选。

### 6.2 加密层 — TLS 1.3

所有公网传输默认走 TLS 1.3：

```bash
# 一键开启（首次运行自动生成证书）
tunnel -L 3306:db:3306 --tls
tunnel -R 9090:localhost:8080 -s public.example.com --tls
```

证书自动管理：

```
首次启动（或 --tls 首次使用）：
  1. 生成 Ed25519 身份密钥对
  2. 生成自签 ECDSA P-256 证书（1 年有效）
  3. 持久化到 ~/.config/tunnel/identity/
  4. 展示身份指纹（public key hash）:
     Tunnel identity fingerprint: SHA256:ABC123...

证书目录：
  ~/.config/tunnel/identity/
  ├── identity.key     # Ed25519 私钥（永不过期）
  ├── identity.pub     # Ed25519 公钥（身份指纹）
  ├── cert.pem         # 当前有效证书
  └── cert-key.pem     # 证书私钥
```

- 证书自动轮换（过期前 30 天重新生成），身份密钥不变。
- 身份指纹固定，第一次看到后后续连接对比验证（类似 SSH `known_hosts`）。

### 6.3 认证层

#### 6.3.1 Token 认证（remote 模式默认）

最简单的共享密钥，适用于 remote 穿透场景：

```bash
# 服务端（公网）
tunnel -R 9090:localhost:8080 \
  -s 0.0.0.0:9000 \
  --tls --token "my-secret-123"

# 客户端（NAT 后）
tunnel -R 9090:localhost:8080 \
  -s public.example.com:9000 \
  --tls --token "my-secret-123"
```

认证流程：
```
客户端 → 服务端: TLS 握手完成
客户端 → 服务端: AuthFrame{token: "my-secret-123"}
服务端: 验证 token 一致性
服务端 → 客户端: AuthResult{ok: true}  或 关闭连接
```

Token 仅用于控制连接认证，不用于数据加密（加密由 TLS 承担）。Token 泄露导致未授权通道，但不泄露历史数据（TLS 前向安全）。

#### 6.3.2 身份指纹认证（免共享密钥）

双方首次交换身份指纹即可：

```bash
# 服务端首次启动（显示指纹）
$ tunnel -R 9090:localhost:8080 -s 0.0.0.0:9000 --tls
Tunnel identity fingerprint: SHA256:ABC123...

# 客户端指定信任的服务端指纹
$ tunnel -R 9090:localhost:8080 -s public.example.com:9000 --tls \
  --server-fingerprint SHA256:ABC123...
```

指纹持久化到 `~/.config/tunnel/known_hosts`（类似 SSH），首次连接后自动记忆：

```
public.example.com:9000 SHA256:ABC123...
```

后续连接 `--trust-on-first-use`（TOFU）模式自动校验，指纹不匹配则拒绝连接并报错：

```
ERROR: Remote host fingerprint mismatch!
  Expected: SHA256:ABC123...
  Got:      SHA256:DEF456...
  This may indicate a man-in-the-middle attack.
```

### 6.4 各模式安全要求

| 模式 | 公网场景 | 安全措施 |
|------|----------|----------|
| local | 通过跳板连内网 | 跳板间 `--tls`，客户端到跳板加密 |
| remote | **核心场景**，穿透 NAT | `--tls` + `--token` 或指纹认证 |
| dynamic | 公网 SOCKS5 代理 | `--tls`，SOCKS5 本身可加 `--socks-auth` |

### 6.5 安全性对比

```
                      SSH -R      本工具 plain    本工具 TLS+token
─────────────────────────────────────────────────────────────
加密                   AES-GCM     无               TLS 1.3 (AEAD)
认证                  公钥         无               共享 token / 指纹
前向安全               ✓           N/A              ✓
握手延迟               3-RTT       0                1-RTT
配置开销               CA/密钥管理  零               一个 --token 或零
```

### 6.6 非目标

- 不实现自己的加密协议。全部走 `crypto/tls`。
- 不支持客户端证书 PKI（mTLS）—— 太重。token + TLS 覆盖 99% 场景。
- 不支持 TLS 版本协商——固定 TLS 1.3，不降级。

---

## 7. 连接生命周期

### 7.1 Local

```
STOPPED
  │  autostart / tunnel start
  ▼
LISTENING ────  accept conn ────→  RELAYING
  │                                  │  conn closed
  │                                  ▼
  │                              IDLE (等待下一连接)
  │  stop / error
  ▼
STOPPED
```

### 7.2 Remote

```
STOPPED
  │  start
  ▼
CONNECTING ───  dial server ───→ CONTROL_ESTABLISHED
  │  (重连)                          │  服务端 accept 后发送 NewChannel
  │                                 ▼
  │                             CHANNEL_RELAYING
  │                                 │  channel 关闭
  │                                 ▼
  │                             IDLE (等待下一 channel)
  │  stop / 控制连接断开
  ▼
RECONNECTING ──→ CONNECTING
```

### 7.3 Dynamic

```
STOPPED
  │
  ▼
LISTENING ────  accept ───→ SOCKS_HANDSHAKE
                                │  SOCKS 协商完成
                                ▼
                            RESOLVE (获取目标地址)
                                │  DNS / BIND
                                ▼
                            RELAYING
                                │  conn closed
                                ▼
                            IDLE
```

---

## 8. 断线重连策略

### 8.1 覆盖范围

| 模式 | 重连对象 | 触发条件 |
|------|----------|----------|
| local | 无（listener 异常才重开） | listener 因系统错误关闭 |
| remote | **控制连接**（核心） | TCP 断开 / 心跳超时 / 服务端重启 |
| dynamic | 无 | listener 异常才重开 |

核心防御对象：**remote 模式的控制连接**。这是整个系统的命脉，丢了所有转发全部中断。

### 8.2 重连算法

```
每次重连: delay = min(base_delay * 2^attempt, max_delay)
          delay = delay ± random(0, delay * jitter_factor)
```

默认值（不可配时使用）：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `max_attempts` | `0` (无限) | 永不放弃，直到进程退出或配置变更 |
| `base_delay` | `1s` | 首次重连延迟 |
| `max_delay` | `30s` | 退避上限 |
| `jitter_factor` | `0.2` | 20% 随机抖动，避免 thundering herd |

序列示例：`1s → 2s → 4s → 8s → 16s → 30s → 30s → ...`

### 8.3 断开期间的保障

1. **所有活跃 channel 立即关闭**，上层应用收到 TCP 断开，触发自身重连逻辑。
2. **remote listener 仍在服务端保留注册**，客户端重连后自动重建。
3. **重连成功后的恢复顺序**：
   ```
   控制连接建立 → 心跳启动 → 服务端重建 remote listener → 通道就绪
   ```
   整个过程对连接的外部用户来说，等效于 TCP 断开后重连。
4. **优雅关闭**（SIGTERM）：发送最后 Pong 确认 → 关闭 channel → 断开控制连接。不会 `FIN` 不发直接断。

### 8.4 防抖与边界

- **不重复重连**：同一隧道同时只有一个重连 goroutine，重连中发生状态变更先 cancel 再重启。
- **配置变更中断重连**：`tunnel rm` 或配置移除时，中断等待中的重连直接进入 STOPPED。
- **DNS 变更**：每次重连重新解析 `server` 域名，不缓存 DNS。
- **网络分区**：重连持续进行，分区恢复后自动恢复，无需人工介入。

### 8.5 健康检查

独立于重连，用于**提前发现**问题：

```
每隔 interval: TCP ping local endpoint
连续 N 次失败 → 日志警告 (不触发重连，重连由控制连接断开触发)
```

健康检查与重连解耦——健康检查只报，重连是更底层机制。避免健康检查误判触发不必要的重连。

### 8.6 恢复通知

每次重连成功或失败时输出结构化日志：

```
INFO  tunnel=my-service event=reconnect.attempt attempt=3 state=connecting
INFO  tunnel=my-service event=reconnect.success attempt=3 elapsed=1.2s
WARN  tunnel=my-service event=reconnect.failure attempt=4 error="dial tcp: i/o timeout"
```

`tunnel ls` 中显示状态和重连次数：

```
NAME          MODE    STATUS      UPTIME    RECONNECTS  REMOTE
dev-db        local   LISTENING   12h       0           -
my-service    remote  ESTABLISHED 3m        14          :9090
socks-proxy   dynamic LISTENING   6h        0           -
```

---

## 9. 实现计划

### Phase 1 — Local 转发核心（3 天）

- Go module, 项目结构
- YAML 配置解析
- TCP relay: `Listener → io.CopyBuffer → Dialer`
- 信号处理（SIGTERM 优雅退出）
- CLI: `tunnel start --foreground`, `tunnel stop`

### Phase 2 — Dynamic SOCKS5（2 天）

- SOCKS5 握手实现（RFC 1928）
- TCP relay 连接 SOCKS 解析的目标
- `mode: dynamic` 完整通路

### Phase 3 — Remote 控制连接 + TLS（5 天）

- 二进制帧协议（Ping/Pong, NewChannel, ChannelData, ChannelClose）
- 控制连接建立 + 心跳保活
- 服务端 listener + channel 分发
- 客户端 channel → local target relay
- **断线重连**（指数退避 + jitter + 无限重连）
- **TLS 1.3** 加密层（`crypto/tls` wrap net.Conn）
- **Token 认证**（AuthFrame 验证）
- **身份指纹** 生成 + TOFU known_hosts
- **自动证书** 生成、持久化、轮换

### Phase 4 — Daemon + 管理（3 天）

- 后台 daemon（pidfile）
- Unix socket JSON-RPC control channel
- `tunnel ls / add / rm / logs`
- SIGHUP 热加载

### Phase 5 — 可靠性 + 完善（2 天）

- 健康检查（TCP ping）
- 每 tunnel 指标
- 连接组
- 多控制连接负载均衡
- systemd unit 生成

### Phase 6 — 进阶（2 天）

- 多跳链式转发
- UDP remote
- `splice(2)` / `sendfile(2)` 优化

---

## 10. 技术栈

| 层 | 选型 | 理由 |
|----|------|------|
| 语言 | Go 1.22+ | 静态编译，net 标准库成熟 |
| TLS | `crypto/tls` | stdlib，纯 Go |
| YAML | `gopkg.in/yaml.v3` | 事实标准 |
| CLI | `flag` 标准库 | 零依赖，功能够用 |
| Control | Unix socket + `encoding/json` | 零依赖 |
| Metrics | `expvar` | stdlib，Prometheus 兼容 |
| Build | `CGO_ENABLED=0` | 纯静态链接 |

---

## 11. 性能关注点

| 关注点 | 策略 |
|--------|------|
| buffer 大小 | sync.Pool, 32KB per direction |
| 控制连接瓶颈 | 单条连接多路复用 → 可配 `connections: N` 轮询 |
| SOCKS5 性能 | 解析完直接 relay，不缓存/不审计 |
| remote 通道延迟 | channel 数据在控制连接上直传，不额外 buffering |
| UDP | 单 goroutine event-loop, LRU 地址映射 |
| TLS 开销 | 仅控制连接 / remote 场景；local 纯内网用 plain |

---

## 12. 测试

| Scope | 方法 |
|-------|------|
| TCP relay | listener + dialer 回环，验证字节一致性 |
| SOCKS5 | 标准 SOCKS5 客户端（curl --socks5）握手 + 传输 |
| Remote 帧协议 | 内存 pipe 模拟控制连接，验证 NewChannel / ChannelData 正确性 |
| Remote E2E | 本地起两个 daemon（client + server），建 remote tunnel，外部连接 |
| 重连 | mock net.Conn，控制失败注入，验证退避序列 |
| TLS | 内存生成临时证书，`tls.Dial` + `tls.Listener` |
| 性能 | `BenchmarkRelay` — 对比 plain / TLS / 不同 buffer 大小 |

---

## 13. FAQ

**Q: remote 模式和 SSH -R 有什么区别？**
A: SSH -R 依赖 SSH 服务端有 sshd。本工具 remote 模式不需要 SSH 协议，用自定义轻量帧协议多路复用，控制开销远小于 SSH 通道。支持 UDP、多连接负载均衡、自动证书，这些 SSH 做不到或很麻烦。

**Q: 控制连接断了怎么办？**
A: 客户端会无限重连（`max_attempts: 0`），指数退避 + jitter。重连后服务端自动恢复 remote listener，中间断开的 channel 全部关闭，新连接走新 channel。

**Q: 多路复用会影响性能吗？**
A: 单条控制连接承载多个 channel，Go 的 goroutine 调度 + 独立 buffer 可让每个 channel 互不阻塞。如果单条成为瓶颈，配 `connections: N` 即可轮询分发到多条控制连接。

**Q: 支持动态端口分配吗？**
A: Phase 1 不支持。可后续在 remote 模式下加 `remote: 0`（随机端口），服务端返回实际端口通过控制连接告知客户端。

**Q: 安全性够吗？**
A: plain 适合内网信任环境。TLS 1.3 提供前向安全性。控制连接和数据流共用 TLS 加密，无中间人风险。mTLS 可选双向认证。
