# Tunnel — 端口转发工具

一个轻量级高性能端口转发工具，支持本地转发、远程转发（NAT 穿透）、SOCKS5 动态代理、TLS 加密、守护进程管理等特性。

---

## 目录

- [安装](#安装)
- [命令概览](#命令概览)
- [场景一：本地端口转发](#场景一本地点对点转发)
- [场景二：远程端口转发（NAT 穿透）](#场景二远程端口转发nat-穿透)
- [场景三：SOCKS5 动态代理](#场景三socks5-动态代理)
- [场景四：启用 TLS 加密](#场景四启用-tls-加密)
- [场景五：令牌认证](#场景五令牌认证)
- [场景六：守护进程模式](#场景六守护进程模式)
- [场景七：YAML 配置文件](#场景七yaml-配置文件)
- [场景八：连接分组管理](#场景八连接分组管理)
- [场景九：多跳链式代理](#场景九多跳链式代理)
- [场景十：健康检查](#场景十健康检查)
- [场景十一：多控制连接（并行隧道）](#场景十一多控制连接并行隧道)
- [场景十二：自动重连](#场景十二自动重连)
- [场景十三：systemd 集成](#场景十三systemd-集成)
- [场景十四：组合使用](#场景十四组合使用)
- [完整配置参考](#完整配置参考)
- [常见问题](#常见问题)

---

## 安装

### 从源码编译

```bash
git clone <repo-url>
cd tunnel
go build -o tunnel ./main.go
sudo cp tunnel /usr/local/bin/
```

### 验证安装

```bash
tunnel -h
```

---

## 命令概览

| 命令 | 说明 |
|------|------|
| `tunnel -L [bind:]port:host:hostport` | 本地端口转发 |
| `tunnel -R port:host:hostport -s addr` | 远程端口转发（客户端） |
| `tunnel -s addr` | 远程转发服务端 |
| `tunnel -D port` | SOCKS5 动态代理 |
| `tunnel start [--background]` | 启动守护进程 |
| `tunnel stop` | 停止守护进程 |
| `tunnel ls` | 列出所有隧道 |
| `tunnel add -L/-R/-D ... [--name X]` | 添加隧道 |
| `tunnel rm <name>` | 删除隧道 |
| `tunnel reload` | 热重载配置 |
| `tunnel start-group <group>` | 启动分组 |
| `tunnel stop-group <group>` | 停止分组 |
| `tunnel init --systemd` | 生成 systemd 单元文件 |

全局标志：

| 标志 | 说明 |
|------|------|
| `--tls` | 启用 TLS 1.3 加密 |
| `--token <str>` | 设置认证令牌 |
| `--udp` | UDP 模式（暂未实现） |

---

## 场景一：本地点对点转发

将本地端口的流量转发到目标地址，类似 `ssh -L`。

### 基本用法

将本地 8080 端口转发到 `web.internal:80`：

```bash
tunnel -L 8080:web.internal:80
```

访问 `http://localhost:8080` 即可到达 `web.internal:80`。

### 指定监听地址

绑定到特定 IP（默认 `127.0.0.1`）：

```bash
# 监听所有网卡（0.0.0.0）
tunnel -L 0.0.0.0:8080:web.internal:80

# 监听特定 IP
tunnel -L 192.168.1.100:8080:web.internal:80
```

### 真实场景：访问远程数据库

```bash
# 将远程 MySQL（3306）映射到本地 3306
tunnel -L 3306:db.internal:3306

# 现在本地 MySQL 客户端可直接连接 localhost:3306
mysql -h 127.0.0.1 -P 3306 -u root -p
```

### 真实场景：穿透内网访问服务

```bash
# 将内网服务器的 3000 端口映射到本地
tunnel -L 3000:192.168.1.50:3000

# 浏览器访问 http://localhost:3000
```

### 性能：自动 splice 优化

在 Linux 上，`io.Copy` 自动使用内核 `splice(2)` 零拷贝优化，无需额外配置。

---

## 场景二：远程端口转发（NAT 穿透）

当客户端没有公网 IP 时，通过一台有公网 IP 的中转服务器暴露内网服务。类似 `ssh -R`。

### 架构

```
[内网客户端] --控制连接--> [公网服务器]
     |                             |
     |                      监听 0.0.0.0:9090
     |                             |
  [内网服务 :8080]          [外网用户] --连接--> server:9090
```

### 第一步：启动服务端

在有公网 IP 的机器上运行：

```bash
# 监听 0.0.0.0:9000 等待客户端连接
tunnel -s 0.0.0.0:9000
```

### 第二步：启动客户端

在无公网 IP 的内网机器上运行：

```bash
# 将本地 8080 端口暴露到服务器的 9090 端口
tunnel -R 9090:localhost:8080 -s server.example.com:9000
```

### 访问服务

外部用户直接连接服务器地址即可访问内网服务：

```bash
curl http://server.example.com:9090
```

### 同时暴露多个端口

一个客户端只能注册一个隧道。需要多个隧道时，启动多个客户端进程（或在守护进程配置中添加多条记录）。

---

## 场景三：SOCKS5 动态代理

提供 SOCKS5 代理服务，浏览器或应用配置后，所有流量自动转发。

### 基本用法

```bash
# 在本地 1080 端口启动 SOCKS5 代理
tunnel -D 1080
```

### 配置浏览器

| 浏览器 | 设置路径 |
|--------|----------|
| Chrome | 设置 → 系统 → 代理 → SOCKS5 → `127.0.0.1:1080` |
| Firefox | 设置 → 网络设置 → 手动代理 → SOCKS5 → `127.0.0.1:1080` |
| macOS | 系统偏好设置 → 网络 → 代理 → SOCKS5 |

### 命令行使用

```bash
# 通过 SOCKS5 代理访问
curl --socks5 127.0.0.1:1080 http://example.com

# SSH 通过 SOCKS5 代理
ssh -o ProxyCommand='nc -x 127.0.0.1:1080 %h %p' user@target
```

### 支持的特性

- CONNECT 命令（TCP 隧道）
- 域名、IPv4、IPv6 地址
- 标准 RFC 1928

---

## 场景四：启用 TLS 加密

所有模式均可通过 `--tls` 标志启用 TLS 1.3 加密。默认自动生成自签名证书，也可指定真实证书文件。

### 使用自动生成的自签名证书

```bash
# 本地转发
tunnel -L 8080:web.internal:80 --tls

# 远程转发服务端
tunnel -s 0.0.0.0:9000 --tls

# 远程转发客户端
tunnel -R 9090:localhost:8080 -s server.example.com:9000 --tls --token mytoken
```

工作流程（以本地转发为例）：

```
[客户端] --TLS 1.3 加密--> [tunnel -L --tls :8080] --明文 TCP--> [web.internal:80]
```

- tunnel 监听 `:8080` 时使用 TLS 1.3 加密（`tls.Listen`）
- 客户端必须用 TLS 连接 `localhost:8080`（如 `curl --https` 或 `openssl s_client`）
- tunnel 解密后通过普通 TCP 转发到目标
- 返回路径相反：目标明文 → tunnel 加密 → 客户端

快速验证（本地 echo 服务）：

```bash
# 终端1：用 nc 起一个 TCP echo 服务器
echo "hello" | nc -l -p 9999 &

# 终端2：启动 TLS 隧道，转发 9998 -> 9999
tunnel -L 9998:127.0.0.1:9999 --tls

# 终端3：用 openssl 以 TLS 连接隧道
echo "hello" | openssl s_client -connect 127.0.0.1:9998 -quiet
```

### 指定真实证书

使用 `--tls-cert` 和 `--tls-key` 指定 PEM 格式的证书和私钥文件（支持所有模式）：

```bash
# 本地转发
tunnel -L 443:web.internal:443 --tls --tls-cert /etc/letsencrypt/live/example.com/fullchain.pem --tls-key /etc/letsencrypt/live/example.com/privkey.pem

# 远程转发服务端
tunnel -s 0.0.0.0:9000 --tls --tls-cert /path/to/cert.pem --tls-key /path/to/key.pem

# 远程转发客户端
tunnel -R 9090:localhost:8080 -s server.example.com:9000 --tls --tls-cert /path/to/cert.pem --tls-key /path/to/key.pem
```

也可以通过 YAML 配置文件指定：

```yaml
tunnels:
  - name: secured-proxy
    mode: local
    local: 443
    remote: web.internal:443
    tls: true
    tls_cert: /etc/letsencrypt/live/example.com/fullchain.pem
    tls_key: /etc/letsencrypt/live/example.com/privkey.pem
    autostart: true
```

### TLS 配置说明

- 使用 TLS 1.3（最高安全版本，仅一个加密套件，完美前向安全性）
- 默认自动生成 ECDSA P-256 自签名证书，有效期 1 年
- 客户端默认 `InsecureSkipVerify = true`（自签名证书跳过验证）
- 指定 `--tls-cert` / `--tls-key` 时使用真实证书，客户端无需 `InsecureSkipVerify`
- 本地转发（`-L --tls`）和远程转发（`-R --tls` + `-s --tls`）均支持
- 加密段仅在"客户端 ↔ tunnel"之间，tunnel → 目标为明文 TCP

---

## 场景五：令牌认证

远程转发模式下，可用令牌认证防止未授权客户端连接。

### 服务端设置令牌

```bash
tunnel -s 0.0.0.0:9000 --token "my-secret-token"
```

### 客户端使用令牌

```bash
tunnel -R 9090:localhost:8080 -s server.example.com:9000 --token "my-secret-token"
```

- 令牌不匹配时，连接被拒绝并记录日志
- 客户端自动尝试重连
- 可同时与 `--tls` 组合使用

---

## 场景六：守护进程模式

将隧道作为后台守护进程运行，支持通过命令动态管理隧道。

### 启动守护进程

```bash
# 前台运行（调试用）
tunnel start

# 后台运行
tunnel start --background
```

### 管理隧道

```bash
# 列出所有隧道
tunnel ls

# 添加新隧道（自动启动）
tunnel add -L 3306:db.internal:3306 --name mysql-tunnel

# 添加远程隧道
tunnel add -R 9090:localhost:8080 --name web-tunnel

# 删除隧道
tunnel rm mysql-tunnel

# 停止守护进程
tunnel stop
```

### 热重载配置

修改 YAML 配置文件后无需重启：

```bash
tunnel reload
```

或向守护进程发送 SIGHUP 信号：

```bash
kill -HUP <pid>
```

---

## 场景七：YAML 配置文件

守护进程模式的配置文件位于 `~/.config/tunnel/config.yaml`。

### 完整示例

```yaml
global:
  log_level: info                # 日志级别
  log_file: /var/log/tunnel.log  # 日志文件（可选，默认 stderr）
  control_socket: ~/.local/share/tunnel/control.sock  # 控制 socket 路径
  tls_dir: ~/.config/tunnel/tls  # TLS 证书目录

tunnels:
  - name: web-proxy
    mode: local
    local: 8080
    remote: web.internal:80
    autostart: true
    group: web

  - name: db-access
    mode: local
    local: 0.0.0.0:3306
    remote: db.internal:3306
    autostart: false
    group: database

  - name: remote-web
    mode: remote
    local: 0.0.0.0:9090
    remote: 9090:localhost:8080
    server: server.example.com:9000
    token: mytoken
    tls: true
    autostart: true
    group: production
    connections: 3
    health_check: 10s

  - name: socks-proxy
    mode: dynamic
    local: 1080
    autostart: true

  - name: chain-tunnel
    mode: local
    local: 3000
    remote: target.internal:3000
    hops:
      - jump01.example.com:8080
      - jump02.example.com:8080
    autostart: true
```

### 配置字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `name` | string | 隧道名称（唯一标识） |
| `mode` | string | 模式：`local`、`remote`、`dynamic` |
| `local` | string | 本地监听地址（如 `8080`、`0.0.0.0:8080`） |
| `remote` | string | 模式相关：local 模式为转发目标，remote 模式为端口映射 |
| `server` | string | remote 模式的服务端地址 |
| `token` | string | 认证令牌 |
| `tls` | bool | 是否启用 TLS |
| `protocol` | string | 协议（`tcp`，`udp` 待实现） |
| `autostart` | bool | 是否随守护进程自动启动 |
| `group` | string | 所属分组 |
| `hops` | []string | 多跳链式代理的跳转节点 |
| `connections` | int | 多控制连接数量（仅 remote 客户端） |
| `health_check` | string | 健康检查间隔（如 `10s`、`30s`） |

---

## 场景八：连接分组管理

将隧道分组管理，批量启停。适合按环境（开发/测试/生产）或用途分组。

### 配置文件分组

```yaml
tunnels:
  - name: dev-api
    group: development
    mode: local
    local: 8080
    remote: dev-api.internal:8080
    autostart: true

  - name: dev-db
    group: development
    mode: local
    local: 3306
    remote: dev-db.internal:3306
    autostart: true

  - name: prod-api
    group: production
    mode: local
    local: 8080
    remote: prod-api.internal:8080
    autostart: false
```

### 管理分组

```bash
# 启动指定分组的所有隧道
tunnel start-group development

# 停止指定分组的所有隧道
tunnel stop-group production

# 不指定 autostart 时通过分组手动管理
```

只有 `autostart: false` 的隧道不会随守护进程自动启动，但可通过分组启动。

---

## 场景九：多跳链式代理

通过多个中间节点转发流量，适合多层网络隔离的环境。

### 架构

```
[客户端] -> jump01:8080 -> jump02:8080 -> target:3000
```

### 配置方式

```yaml
tunnels:
  - name: multi-hop
    mode: local
    local: 3000
    remote: final-target:3000
    hops:
      - jump01.example.com:8080
      - jump02.example.com:8080
    autostart: true
```

每个跳转节点上需要先运行对应的本地转发放服务。

### 命令行模式

暂不支持命令行多跳，需使用配置文件 + 守护进程模式。

---

## 场景十：健康检查

对隧道监听地址执行定期 TCP 连接检查，不可达时输出告警日志。

### 配置方式

在隧道配置中添加 `health_check` 字段：

```yaml
tunnels:
  - name: monitored-proxy
    mode: local
    local: 8080
    remote: web.internal:80
    health_check: 10s
    autostart: true
```

### 行为说明

- 每 10 秒尝试建立 TCP 连接到 `local` 地址
- 连续 3 次失败后输出 `health: down` 警告
- 恢复后输出 `health: recovered`
- 仅记录日志，不做自动重启（可通过日志监控工具配合处理）

---

## 场景十一：多控制连接（并行隧道）

远程转发模式下，可建立多条并行控制连接提高吞吐量。

### 配置方式

```yaml
tunnels:
  - name: high-throughput-remote
    mode: remote
    server: server.example.com:9000
    remote: 9090:localhost:8080
    connections: 3
    autostart: true
```

### 说明

- 建立 N 条独立控制连接到服务端
- 每条连接独立处理通道请求
- 适合高并发场景
- 最大限制 10 条
- 服务端只需一个，客户端会自动均衡

---

## 场景十二：自动重连

远程转发客户端在连接断开时自动重连。

### 重连策略

- **初始延迟**: 1 秒
- **最大延迟**: 30 秒
- **退避算法**: 指数退避（每次翻倍）
- **抖动**: 20% 随机抖动（避免惊群）
- **限制**: 最多 30 次翻倍（约 30 秒封顶）
- **重试**: 无限重试，永不放弃

### 日志示例

```
remote: connection failed (dial tcp server:9000: connection refused), reconnecting in 1.083335103s (attempt 1)
remote: connection failed (dial tcp server:9000: connection refused), reconnecting in 2.171235028s (attempt 2)
...
remote: connected to server:9000, 1 tunnels registered
```

重连机制在 `RemoteClient` 内部自动启用，用户无需额外配置。

---

## 场景十三：systemd 集成

将守护进程注册为 systemd 服务，实现开机自启、自动重启。

### 生成单元文件

```bash
# 生成系统级单元文件（需要 root）
tunnel init --systemd | sudo tee /etc/systemd/system/tunnel.service

# 生成用户级单元文件（无需 root）
tunnel init --systemd-user
```

### 启用服务

```bash
# 系统级
sudo systemctl daemon-reload
sudo systemctl enable --now tunnel

# 用户级
systemctl --user daemon-reload
systemctl --user enable --now tunnel
```

### 管理服务

```bash
# 查看状态
systemctl status tunnel

# 重启
systemctl restart tunnel

# 重新加载配置
systemctl reload tunnel
```

### 查看安装提示

```bash
tunnel init
```

---

## 场景十四：组合使用

### 场景：暴露内网 Web 服务到公网（安全模式）

```yaml
# 服务端（公网服务器，/etc/tunnel/config.yaml）
tunnels:
  - name: server
    mode: remote
    local: 0.0.0.0:9000
    token: strong-token-here
    tls: true
    autostart: true
```

```bash
# 服务端启动
sudo tunnel start --background

# 客户端（内网机器）
tunnel -R 8443:localhost:443 -s server.example.com:9000 --tls --token strong-token-here
```

现在 `https://server.example.com:8443` 可访问内网 HTTPS 服务。

### 场景：开发环境一键启动

```yaml
tunnels:
  - name: api
    mode: local
    local: 3000
    remote: dev-api:3000
    group: development
    autostart: true

  - name: db
    mode: local
    local: 5432
    remote: dev-db:5432
    group: development
    autostart: true

  - name: redis
    mode: local
    local: 6379
    remote: dev-redis:6379
    group: development
    autostart: true
```

```bash
tunnel start --background
# 所有开发隧道自动启动

# 下班时一键停止
tunnel stop-group development
```

### 场景：通过跳板机访问内部数据库

```yaml
tunnels:
  - name: db-proxy
    mode: local
    local: 3306
    remote: internal-db:3306
    hops:
      - bastion.example.com:2222
    autostart: true
```

```bash
tunnel start --background
mysql -h 127.0.0.1 -P 3306
```

### 场景：SOCKS5 + 远程隧道组合

在内网机器上启动 SOCKS5 代理，再通过远程隧道暴露到公网：

```yaml
# 内网机器的配置
tunnels:
  - name: socks
    mode: dynamic
    local: 1080
    autostart: true

  - name: tunnel
    mode: remote
    server: public.example.com:9000
    remote: 1080:localhost:1080
    autostart: true
```

公网用户配置 SOCKS5 代理为 `public.example.com:1080` 即可使用内网网络。

---

## 完整配置参考

```yaml
# ~/.config/tunnel/config.yaml

global:
  log_level: info                # debug | info | warn | error
  control_socket: ~/.local/share/tunnel/control.sock

tunnels:
  # 本地转发
  - name: example-local
    mode: local
    local: 8080                              # 或 0.0.0.0:8080
    remote: target.internal:80
    autostart: true
    group: ""
    hops: []
    health_check: ""

  # 远程转发（服务端）
  - name: example-server
    mode: remote
    local: 0.0.0.0:9000
    token: ""
    tls: false
    autostart: true

  # 远程转发（客户端）
  - name: example-client
    mode: remote
    server: server.example.com:9000
    remote: 9090:localhost:8080
    token: "my-token"
    tls: true
    connections: 1
    autostart: true

  # SOCKS5 动态代理
  - name: example-socks
    mode: dynamic
    local: 1080
    autostart: true
```

---

## 常见问题

### Q: 端口被占用怎么办？

A: 更换端口号。如果使用守护进程模式，先 `tunnel stop` 再修改配置重启。

### Q: 远程转发连接被拒绝？

A: 检查：
- 服务端是否可访问（ping / telnet）
- 服务端是否在用 `-s` 模式运行
- 令牌是否匹配
- 防火墙是否放行端口

### Q: 如何查看日志？

A: 守护进程模式下日志输出到 stderr（前台）或配置的 `log_file`（后台）。可用 `journalctl -u tunnel` 查看。

### Q: 客户端和服务端版本一定要一致吗？

A: 强烈建议使用同一版本编译的二进制。帧协议可能随版本变化。

### Q: 支持 UDP 转发吗？

A: 暂不支持。`--udp` 标志预留但未实现。

### Q: 和 ssh -L/-R/-D 有什么区别？

A: 不依赖 SSH 协议，更轻量。支持自动重连、多控制连接、健康检查、多跳链式代理等 SSH 不具备的功能。TLS 加密而非 SSH 加密。

### Q: 如何保证连接安全？

A: 建议同时使用 `--tls`（TLS 1.3 加密）和 `--token`（令牌认证）。不要在不安全的网络上使用明文模式。

### Q: 并发性能如何？

A: 每个连接由独立 goroutine 处理。Linux 上自动使用 `splice(2)` 零拷贝。远程转发支持多控制连接（`connections` 参数）提高吞吐量。
