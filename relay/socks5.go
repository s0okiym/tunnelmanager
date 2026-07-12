package relay

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

const (
	socksVer5          = 5
	socksCmdConnect    = 1
	socksAtypIPv4      = 1
	socksAtypDomain    = 3
	socksAtypIPv6      = 4
	socksAuthNone      = 0
	socksAuthPassword  = 2
	socksAuthNoAccept  = 0xff
	socksRepSuccess    = 0
	socksRepAuthFailed = 2
)

type socksTarget struct {
	raw  []byte // atyp + addr + port (for response echo)
	addr string
}

func socksHandshake(conn net.Conn, user, pass string) (*socksTarget, error) {
	requireAuth := user != "" || pass != ""

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

	// Choose authentication method.
	var chosen byte = socksAuthNone
	if requireAuth {
		chosen = socksAuthNoAccept
		for _, m := range methods {
			if m == socksAuthPassword {
				chosen = socksAuthPassword
				break
			}
		}
	} else {
		for _, m := range methods {
			if m == socksAuthNone {
				chosen = socksAuthNone
				break
			}
		}
	}
	if _, err := conn.Write([]byte{socksVer5, chosen}); err != nil {
		return nil, fmt.Errorf("socks auth response: %w", err)
	}
	if chosen == socksAuthNoAccept {
		return nil, fmt.Errorf("socks: no acceptable auth method")
	}

	if chosen == socksAuthPassword {
		if err := socksPasswordAuth(conn, user, pass); err != nil {
			return nil, err
		}
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

// socksPasswordAuth performs RFC 1929 username/password subnegotiation.
func socksPasswordAuth(conn net.Conn, user, pass string) error {
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return fmt.Errorf("socks password auth: %w", err)
	}
	if header[0] != 1 { // subnegotiation version
		conn.Write([]byte{1, 1})
		return fmt.Errorf("socks password auth: unsupported version %d", header[0])
	}
	userLen := int(header[1])
	if userLen < 1 || userLen > 255 {
		conn.Write([]byte{1, 2})
		return fmt.Errorf("socks password auth: invalid username length %d", userLen)
	}
	userBuf := make([]byte, userLen)
	if _, err := io.ReadFull(conn, userBuf); err != nil {
		return fmt.Errorf("socks password auth username: %w", err)
	}
	var plen [1]byte
	if _, err := io.ReadFull(conn, plen[:]); err != nil {
		return fmt.Errorf("socks password auth password len: %w", err)
	}
	passLen := int(plen[0])
	if passLen < 1 || passLen > 255 {
		conn.Write([]byte{1, 2})
		return fmt.Errorf("socks password auth: invalid password length %d", passLen)
	}
	passBuf := make([]byte, passLen)
	if _, err := io.ReadFull(conn, passBuf); err != nil {
		return fmt.Errorf("socks password auth password: %w", err)
	}

	if string(userBuf) != user || string(passBuf) != pass {
		conn.Write([]byte{1, 1})
		return fmt.Errorf("socks: authentication failed")
	}
	if _, err := conn.Write([]byte{1, 0}); err != nil {
		return fmt.Errorf("socks password auth success: %w", err)
	}
	return nil
}

func socksReply(conn net.Conn, rep byte, atyp byte, addr []byte, port []byte) error {
	msg := append([]byte{socksVer5, rep, 0, atyp}, addr...)
	msg = append(msg, port...)
	_, err := conn.Write(msg)
	return err
}
