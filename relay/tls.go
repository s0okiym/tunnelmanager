package relay

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

type TLSConfig struct {
	Enabled  bool
	Cert     tls.Certificate
	Insecure bool
}

func NewTLSConfig(insecure bool) *TLSConfig {
	return &TLSConfig{Enabled: true, Insecure: insecure}
}

func GenerateCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "tunnel",
			Organization: []string{"tunnel"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create cert: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}, nil
}

func TLSListener(addr string, cfg *TLSConfig) (net.Listener, error) {
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cfg.Cert},
		MinVersion:   tls.VersionTLS13,
	}
	if cfg.Insecure {
		tlsCfg.InsecureSkipVerify = true
	}
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
	if cfg.Insecure {
		tlsCfg.InsecureSkipVerify = true
	}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("tls dial: %w", err)
	}
	return conn, nil
}
