package relay

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrUnknownHost is returned by KnownHosts.Verify when the host has no recorded
// fingerprint.
var ErrUnknownHost = errors.New("unknown host")

// KnownHosts stores trusted server fingerprints, similar to SSH known_hosts.
type KnownHosts struct {
	mu      sync.Mutex
	path    string
	entries map[string]string // host:port -> fingerprint
}

// LoadKnownHosts loads a known-hosts file. A missing file is treated as an empty
// store, not an error.
func LoadKnownHosts(path string) (*KnownHosts, error) {
	kh := &KnownHosts{
		path:    path,
		entries: make(map[string]string),
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return kh, nil
		}
		return nil, fmt.Errorf("open known_hosts: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		host := fields[0]
		fp := fields[1]
		if !strings.HasPrefix(fp, "SHA256:") {
			continue
		}
		kh.entries[host] = fp
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return kh, nil
}

// Verify returns nil if the host fingerprint matches a recorded entry. If the
// host has no recorded entry, it returns ErrUnknownHost. Use Accept to record a
// new host.
func (kh *KnownHosts) Verify(host, fingerprint string) error {
	kh.mu.Lock()
	defer kh.mu.Unlock()

	recorded, ok := kh.entries[host]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownHost, host)
	}
	if recorded != fingerprint {
		return fmt.Errorf("host %q fingerprint mismatch: expected %s, got %s", host, recorded, fingerprint)
	}
	return nil
}

// Accept records a host fingerprint if it is not already recorded, or if it
// matches the existing recorded fingerprint. It returns an error if the host is
// already recorded with a different fingerprint.
func (kh *KnownHosts) Accept(host, fingerprint string) error {
	kh.mu.Lock()
	defer kh.mu.Unlock()

	if recorded, ok := kh.entries[host]; ok && recorded != fingerprint {
		return fmt.Errorf("host %q fingerprint mismatch: expected %s, got %s", host, recorded, fingerprint)
	}
	kh.entries[host] = fingerprint
	return kh.saveLocked()
}

func (kh *KnownHosts) saveLocked() error {
	if kh.path == "" {
		return fmt.Errorf("known_hosts path not set")
	}
	if err := os.MkdirAll(filepath.Dir(kh.path), 0700); err != nil {
		return fmt.Errorf("known_hosts mkdir: %w", err)
	}

	f, err := os.Create(kh.path)
	if err != nil {
		return fmt.Errorf("create known_hosts: %w", err)
	}
	defer f.Close()

	for host, fp := range kh.entries {
		if _, err := fmt.Fprintf(f, "%s %s\n", host, fp); err != nil {
			return err
		}
	}
	return nil
}

// Path returns the file path backing this store.
func (kh *KnownHosts) Path() string {
	kh.mu.Lock()
	defer kh.mu.Unlock()
	return kh.path
}
