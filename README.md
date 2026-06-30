# Tunnel Manager

A lightweight, high-performance port forwarding tool. Single binary, zero dependencies.

## Quick start

```bash
# Local port forwarding (like ssh -L)
tunnel -L 3306:db.internal:3306

# With bind address
tunnel -L 127.0.0.1:3306:db.internal:3306

# Multiple tunnels
tunnel -L 3306:db:3306 -L 8080:web:80

# Ctrl+C to stop
```

## Features

- **Local forwarding** (`-L`) — forward a local port to a remote target
- **Half-close semantics** — correct bidirectional relay without deadlocks
- **Buffer pool** — 32KB buffers per direction, reused via `sync.Pool`
- **Signal handling** — graceful shutdown on SIGINT/SIGTERM
- **No dependencies** — pure Go standard library

### Planned

- SOCKS5 dynamic proxy (`-D`)
- Remote reverse forwarding (`-R`) for NAT penetration
- TLS 1.3 encryption with auto-generated certificates
- Daemon mode with YAML config management

## Install

```bash
go install github.com/s0okiym/tunnelmanager@latest
```

Or build from source:

```bash
git clone https://github.com/s0okiym/tunnelmanager.git
cd tunnelmanager
go build -o tunnel .
```

## Usage

```
tunnel -L [bind:]port:host:hostport    Local forwarding
```

| Argument | Description |
|----------|-------------|
| `-L [bind:]port:host:hostport` | Listen on `bind:port`, forward to `host:hostport` |

## Development

```bash
go test -race -count=1 ./...
```

## License

MIT
