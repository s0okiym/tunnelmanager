package relay

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadOrGenerateIdentity(t *testing.T) {
	dir := t.TempDir()

	cert1, fp1, err := LoadOrGenerateIdentity(dir)
	if err != nil {
		t.Fatalf("first load/generate: %v", err)
	}
	if fp1 == "" || !strings.HasPrefix(fp1, "SHA256:") {
		t.Fatalf("unexpected fingerprint format: %q", fp1)
	}

	cert2, fp2, err := LoadOrGenerateIdentity(dir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if fp1 != fp2 {
		t.Fatalf("fingerprint changed across loads: %q vs %q", fp1, fp2)
	}
	_ = cert2

	for _, name := range []string{"cert.pem", "cert-key.pem"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("identity file %s not saved: %v", name, err)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm()&0077 != 0 {
			t.Fatalf("identity file %s is world/group accessible: %v", name, info.Mode())
		}
	}

	leaf, err := x509.ParseCertificate(cert1.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if time.Now().After(leaf.NotAfter) {
		t.Fatal("generated certificate is already expired")
	}
}

func TestPublicKeyFingerprintFormat(t *testing.T) {
	cert, err := GenerateCert()
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	fp, err := PublicKeyFingerprint(leaf.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("expected SHA256: prefix, got %q", fp)
	}
}

func TestIdentityRotationPreservesFingerprint(t *testing.T) {
	dir := t.TempDir()

	// First generation.
	cert1, fp1, err := LoadOrGenerateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Force rotation by backdating the certificate to be within the rotation
	// window. Keep the original key file.
	leaf, err := x509.ParseCertificate(cert1.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	leaf.NotAfter = time.Now().Add(rotationWindow - time.Hour)
	backdatedDER, err := x509.CreateCertificate(rand.Reader, leaf, leaf, leaf.PublicKey, cert1.PrivateKey)
	if err != nil {
		t.Fatalf("backdate cert: %v", err)
	}
	certPEM := pemEncode("CERTIFICATE", backdatedDER)
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0600); err != nil {
		t.Fatal(err)
	}

	// Clear cache to simulate a fresh process load.
	identityMu.Lock()
	delete(identityCache, dir)
	identityMu.Unlock()

	cert2, fp2, err := LoadOrGenerateIdentity(dir)
	if err != nil {
		t.Fatalf("load after backdating: %v", err)
	}
	if fp1 != fp2 {
		t.Fatalf("rotation changed fingerprint: %q vs %q", fp1, fp2)
	}

	leaf2, err := x509.ParseCertificate(cert2.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if time.Now().After(leaf2.NotAfter) {
		t.Fatal("rotated certificate is already expired")
	}
	if time.Until(leaf2.NotAfter) < rotationWindow {
		t.Fatal("rotated certificate is still within rotation window")
	}
}

func TestIdentityNotRotatedIfValid(t *testing.T) {
	dir := t.TempDir()

	cert1, _, err := LoadOrGenerateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	serial1, err := x509.ParseCertificate(cert1.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	identityMu.Lock()
	delete(identityCache, dir)
	identityMu.Unlock()

	cert2, _, err := LoadOrGenerateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	serial2, err := x509.ParseCertificate(cert2.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if serial1.SerialNumber.Cmp(serial2.SerialNumber) != 0 {
		t.Fatal("valid certificate was unexpectedly rotated")
	}
}

func pemEncode(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
