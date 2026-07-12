package relay

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
)

type TLSConfig struct {
	Enabled           bool
	Cert              tls.Certificate
	Insecure          bool
	CertFile          string
	KeyFile           string
	Fingerprint       string // server identity fingerprint, for logging
	TrustOnFirstUse   bool
	ServerFingerprint string
	KnownHosts        *KnownHosts
}

func NewTLSConfig(insecure bool) *TLSConfig {
	return &TLSConfig{Enabled: true, Insecure: insecure}
}

// SetupTLS returns a TLSConfig. If certFile and keyFile are non-empty, loads
// them via tls.LoadX509KeyPair; otherwise uses a persistent auto-generated
// identity if DefaultIdentityDir is set, falling back to an ephemeral cert.
// When verify is true, Insecure is set to false (peer certificate is verified).
// When verify is false, Insecure is set to true (peer certificate is skipped).
func SetupTLS(certFile, keyFile string, verify bool) (*TLSConfig, error) {
	var cert tls.Certificate
	var fp string
	var err error
	if certFile != "" && keyFile != "" {
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("tls cert: %w", err)
		}
		fp, err = tlsCertFingerprint(cert)
		if err != nil {
			return nil, fmt.Errorf("tls fingerprint: %w", err)
		}
	} else {
		cert, fp, err = LoadOrGenerateIdentity(DefaultIdentityDir)
		if err != nil {
			return nil, fmt.Errorf("tls identity: %w", err)
		}
	}
	return &TLSConfig{Enabled: true, Cert: cert, Insecure: !verify, Fingerprint: fp}, nil
}

// SetupTLSCert returns tls.Certificate from files or the persistent auto-generated
// identity as fallback.
func SetupTLSCert(certFile, keyFile string) (tls.Certificate, string, error) {
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		fp, err := tlsCertFingerprint(cert)
		return cert, fp, err
	}
	return LoadOrGenerateIdentity(DefaultIdentityDir)
}

func TLSListener(addr string, cfg *TLSConfig) (net.Listener, error) {
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cfg.Cert},
		MinVersion:   tls.VersionTLS13,
	}
	// InsecureSkipVerify only affects client-side verification; it is ignored
	// by tls.Listen. We intentionally do not set it here.
	ln, err := tls.Listen("tcp", addr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("tls listen: %w", err)
	}
	return ln, nil
}

func TLSDial(addr string, cfg *TLSConfig) (net.Conn, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
	manualVerify := cfg.TrustOnFirstUse || cfg.ServerFingerprint != ""
	if cfg.Insecure || manualVerify {
		tlsCfg.InsecureSkipVerify = true
	}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("tls dial: %w", err)
	}

	if manualVerify {
		if err := verifyServerFingerprint(addr, conn, cfg); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func verifyServerFingerprint(addr string, conn *tls.Conn, cfg *TLSConfig) error {
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return fmt.Errorf("tls: server presented no certificate")
	}
	leaf := state.PeerCertificates[0]
	fp := PeerFingerprint(leaf)

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	key := addr

	if cfg.ServerFingerprint != "" {
		if fp != cfg.ServerFingerprint {
			return fmt.Errorf("tls: server %s fingerprint mismatch: expected %s, got %s", host, cfg.ServerFingerprint, fp)
		}
		return nil
	}

	if cfg.KnownHosts == nil {
		return fmt.Errorf("tls: trust-on-first-use requested but no known_hosts store configured")
	}
	if err := cfg.KnownHosts.Verify(key, fp); err != nil {
		if errors.Is(err, ErrUnknownHost) {
			if aerr := cfg.KnownHosts.Accept(key, fp); aerr != nil {
				return fmt.Errorf("tls: trust-on-first-use failed: %w", aerr)
			}
			return nil
		}
		return fmt.Errorf("tls: %w", err)
	}
	return nil
}
