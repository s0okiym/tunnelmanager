# Tunnel Manager

轻量、高性能的端口转发工具。单二进制，零外部依赖。

## 快速开始

```bash
# 本地端口转发（类似 ssh -L）
tunnel -L 3306:db.internal:3306

# 指定绑定地址
tunnel -L 127.0.0.1:3306:db.internal:3306

# 多条转发
tunnel -L 3306:db:3306 -L 8080:web:80

# Ctrl+C 停止
```

## 功能

- **本地转发**（`-L`）— 将本地端口转发到远程目标
- **半关闭语义** — 正确的双向转发，无死锁
- **缓冲池** — 每方向 32KB 缓冲区，通过 `sync.Pool` 复用
- **信号处理** — SIGINT/SIGTERM 优雅退出
- **纯标准库** — 仅使用 Go 标准库，零依赖

### 规划中

- SOCKS5 动态代理（`-D`）
- 远程反向转发（`-R`）用于 NAT 穿透
- TLS 1.3 加密 + 自动证书
- Daemon 模式 + YAML 配置管理

## 安装

```bash
go install github.com/s0okiym/tunnelmanager@latest
```

或从源码构建：

```bash
git clone https://github.com/s0okiym/tunnelmanager.git
cd tunnelmanager
go build -o tunnel .
```

## 使用

```
tunnel -L [bind:]port:host:hostport    本地端口转发
```

| 参数 | 说明 |
|------|------|
| `-L [bind:]port:host:hostport` | 监听 `bind:port`，转发到 `host:hostport` |

## 开发

```bash
go test -race -count=1 ./...
```

## License

MIT
