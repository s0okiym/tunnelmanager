package relay

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

const (
	socksVer5       = 5
	socksCmdConnect = 1
	socksAtypIPv4   = 1
	socksAtypDomain = 3
	socksAtypIPv6   = 4
	socksAuthNone   = 0
	socksRepSuccess = 0
)

type socksTarget struct {
	raw  []byte // atyp + addr + port (for response echo)
	addr string
}

func socksHandshake(conn net.Conn) (*socksTarget, error) {
	// greeting: ver, nmethods, methods[]
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, fmt.Errorf("socks greeting: %w", err)
	}
	if header[0] != socksVer5 {
		return nil, fmt.Errorf("socks: unsupported version %d", header[0])
	}
	nmethods := int(header[1])
	if nmethods < 1 {
		return nil, fmt.Errorf("socks: no auth methods")
	}
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return nil, fmt.Errorf("socks methods: %w", err)
	}

	// respond: no auth required
	if _, err := conn.Write([]byte{socksVer5, socksAuthNone}); err != nil {
		return nil, fmt.Errorf("socks auth response: %w", err)
	}

	// request: ver, cmd, rsv, atyp, dst...
	var req [4]byte
	if _, err := io.ReadFull(conn, req[:]); err != nil {
		return nil, fmt.Errorf("socks request: %w", err)
	}
	if req[0] != socksVer5 {
		return nil, fmt.Errorf("socks: request version %d", req[0])
	}
	if req[1] != socksCmdConnect {
		return nil, fmt.Errorf("socks: unsupported command %d", req[1])
	}

	atyp := req[3]
	var target socksTarget

	switch atyp {
	case socksAtypIPv4:
		var addr [4]byte
		if _, err := io.ReadFull(conn, addr[:]); err != nil {
			return nil, fmt.Errorf("socks ipv4: %w", err)
		}
		target.raw = append(target.raw, atyp)
		target.raw = append(target.raw, addr[:]...)
		target.addr = net.IP(addr[:]).String()

	case socksAtypIPv6:
		var addr [16]byte
		if _, err := io.ReadFull(conn, addr[:]); err != nil {
			return nil, fmt.Errorf("socks ipv6: %w", err)
		}
		target.raw = append(target.raw, atyp)
		target.raw = append(target.raw, addr[:]...)
		target.addr = net.IP(addr[:]).String()

	case socksAtypDomain:
		var lenByte [1]byte
		if _, err := io.ReadFull(conn, lenByte[:]); err != nil {
			return nil, fmt.Errorf("socks domain len: %w", err)
		}
		domainLen := int(lenByte[0])
		domain := make([]byte, domainLen)
		if _, err := io.ReadFull(conn, domain); err != nil {
			return nil, fmt.Errorf("socks domain: %w", err)
		}
		target.raw = append(target.raw, atyp, lenByte[0])
		target.raw = append(target.raw, domain...)
		target.addr = string(domain)

	default:
		return nil, fmt.Errorf("socks: unknown address type %d", atyp)
	}

	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return nil, fmt.Errorf("socks port: %w", err)
	}
	target.raw = append(target.raw, port[:]...)
	target.addr = net.JoinHostPort(target.addr, fmt.Sprintf("%d", binary.BigEndian.Uint16(port[:])))

	return &target, nil
}

func socksReply(conn net.Conn, rep byte, atyp byte, addr []byte, port []byte) error {
	msg := append([]byte{socksVer5, rep, 0, atyp}, addr...)
	msg = append(msg, port...)
	_, err := conn.Write(msg)
	return err
}
