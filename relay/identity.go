package relay

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

// DefaultIdentityDir is the directory where the persistent auto-generated TLS
// identity is stored. It is set by the CLI/daemon at startup.
var DefaultIdentityDir string

// rotationWindow is how long before expiry we regenerate the certificate. This
// keeps a long-running server from hitting an expired cert at runtime.
const rotationWindow = 30 * 24 * time.Hour

type identityCacheEntry struct {
	cert tls.Certificate
	fp   string
	err  error
}

var (
	identityMu    sync.Mutex
	identityCache = map[string]*identityCacheEntry{}
)

// LoadOrGenerateIdentity returns a persistent TLS certificate for the given
// directory. If the directory contains a valid cert.pem/key.pem pair, it is
// loaded; otherwise a fresh self-signed ECDSA P-256 certificate is generated
// using the persisted key (or a new key if none exists), written to disk, and
// returned. The result is cached per directory so multiple tunnels in the same
// process share one identity. Certificates are rotated when they are within
// rotationWindow of expiry.
func LoadOrGenerateIdentity(dir string) (tls.Certificate, string, error) {
	if dir == "" {
		cert, err := GenerateCert()
		if err != nil {
			return tls.Certificate{}, "", err
		}
		fp, err := tlsCertFingerprint(cert)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		return cert, fp, nil
	}

	identityMu.Lock()
	defer identityMu.Unlock()

	if entry, ok := identityCache[dir]; ok {
		if entry.err != nil || entry.cert.Certificate == nil || !certNeedsRotation(entry.cert) {
			return entry.cert, entry.fp, entry.err
		}
		// Cached cert is close to expiry; force regeneration.
		delete(identityCache, dir)
	}

	entry := &identityCacheEntry{}
	entry.cert, entry.fp, entry.err = loadOrGenerateIdentityLocked(dir)
	identityCache[dir] = entry
	return entry.cert, entry.fp, entry.err
}

func loadOrGenerateIdentityLocked(dir string) (tls.Certificate, string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("identity mkdir: %w", err)
	}

	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "cert-key.pem")

	key, err := loadIdentityKey(keyPath)
	if err != nil {
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return tls.Certificate{}, "", fmt.Errorf("generate identity key: %w", err)
		}
		if err := saveIdentityKey(key, keyPath); err != nil {
			return tls.Certificate{}, "", fmt.Errorf("save identity key: %w", err)
		}
	}

	cert, err := tryLoadCert(certPath, keyPath, key)
	if err == nil {
		fp, err := publicKeyFingerprint(&key.PublicKey)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		return cert, fp, nil
	}

	cert, err = generateCertWithKey(key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("generate identity cert: %w", err)
	}
	if err := saveIdentityCert(cert, certPath); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("save identity cert: %w", err)
	}

	fp, err := publicKeyFingerprint(&key.PublicKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, fp, nil
}

func loadIdentityKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM data in %s", path)
	}
	switch block.Type {
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		ec, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("expected ECDSA key, got %T", key)
		}
		return ec, nil
	default:
		return nil, fmt.Errorf("unsupported key type %q", block.Type)
	}
}

func saveIdentityKey(key *ecdsa.PrivateKey, path string) error {
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	return os.WriteFile(path, keyPEM, 0600)
}

func tryLoadCert(certPath, keyPath string, key *ecdsa.PrivateKey) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if len(cert.Certificate) == 0 {
		return tls.Certificate{}, fmt.Errorf("no certificates in identity")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, err
	}
	if time.Now().After(leaf.NotAfter) {
		return tls.Certificate{}, fmt.Errorf("identity certificate expired")
	}
	if certNeedsRotation(cert) {
		return tls.Certificate{}, fmt.Errorf("identity certificate near expiry")
	}
	if !reflect.DeepEqual(leaf.PublicKey, &key.PublicKey) {
		return tls.Certificate{}, fmt.Errorf("certificate public key does not match identity key")
	}
	return cert, nil
}

func certNeedsRotation(cert tls.Certificate) bool {
	if len(cert.Certificate) == 0 {
		return true
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return true
	}
	return time.Until(leaf.NotAfter) < rotationWindow
}

func saveIdentityCert(cert tls.Certificate, certPath string) error {
	if len(cert.Certificate) == 0 {
		return fmt.Errorf("no certificate to save")
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	return os.WriteFile(certPath, certPEM, 0600)
}

// GenerateCert returns a fresh ephemeral ECDSA P-256 self-signed certificate.
func GenerateCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate key: %w", err)
	}
	return generateCertWithKey(key)
}

func generateCertWithKey(key *ecdsa.PrivateKey) (tls.Certificate, error) {
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

// PublicKeyFingerprint returns a stable fingerprint string for a public key in
// the form SHA256:<base64(PKIX public key DER)>.
func PublicKeyFingerprint(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("marshal public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

// PeerFingerprint returns the public-key fingerprint of the leaf certificate.
func PeerFingerprint(cert *x509.Certificate) string {
	fp, err := PublicKeyFingerprint(cert.PublicKey)
	if err != nil {
		return ""
	}
	return fp
}

func publicKeyFingerprint(pub crypto.PublicKey) (string, error) {
	return PublicKeyFingerprint(pub)
}

func tlsCertFingerprint(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", fmt.Errorf("no certificate")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return "", err
	}
	return PublicKeyFingerprint(leaf.PublicKey)
}
