package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnownHostsLoadSaveVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	kh, err := LoadKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := kh.Verify("example.com:9000", "SHA256:AAA"); err == nil {
		t.Fatal("expected error for unknown host")
	}
	if err := kh.Accept("example.com:9000", "SHA256:AAA"); err != nil {
		t.Fatal(err)
	}
	if err := kh.Verify("example.com:9000", "SHA256:AAA"); err != nil {
		t.Fatalf("verify after accept: %v", err)
	}

	loaded, err := LoadKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.Verify("example.com:9000", "SHA256:AAA"); err != nil {
		t.Fatalf("verify from disk: %v", err)
	}
}

func TestKnownHostsMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")

	kh, err := LoadKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := kh.Accept("example.com:9000", "SHA256:AAA"); err != nil {
		t.Fatal(err)
	}
	if err := kh.Verify("example.com:9000", "SHA256:BBB"); err == nil {
		t.Fatal("expected mismatch error")
	} else if !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected mismatch in error, got %v", err)
	}
}

func TestKnownHostsIgnoresCommentsAndBlank(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	data := "# comment\n\nexample.com:9000 SHA256:CCC\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	kh, err := LoadKnownHosts(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := kh.Verify("example.com:9000", "SHA256:CCC"); err != nil {
		t.Fatal(err)
	}
}
