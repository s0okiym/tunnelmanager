package manager

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

// defaultLogBuffer is the process-wide log ring buffer used by the daemon.
// It is populated by InstallLogCapture.
var defaultLogBuffer = NewLogBuffer(1000)

// GlobalLogBuffer returns the daemon's in-memory log ring buffer.
func GlobalLogBuffer() *LogBuffer {
	return defaultLogBuffer
}

// InstallLogCapture duplicates all future stdlib log output to the in-memory
// ring buffer while preserving the original destination (stderr or a log file).
// Call this once in the daemon after any log-file redirection is set up.
func InstallLogCapture() {
	orig := log.Writer()
	if orig == nil {
		orig = os.Stderr
	}
	log.SetOutput(io.MultiWriter(orig, defaultLogBuffer))
}

// LogBuffer is a thread-safe ring buffer of recent log lines.
type LogBuffer struct {
	mu       sync.Mutex
	lines    []string
	capacity int
}

// NewLogBuffer creates a LogBuffer that keeps the most recent capacity lines.
func NewLogBuffer(capacity int) *LogBuffer {
	if capacity <= 0 {
		capacity = 1000
	}
	return &LogBuffer{capacity: capacity}
}

// Write implements io.Writer. It splits p into lines and stores them. This
// method is called by the stdlib log package via InstallLogCapture.
func (lb *LogBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	s := string(p)
	// The log package writes complete lines with a trailing newline, but be
	// defensive about partial writes and multi-line messages.
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for _, line := range parts {
		if line == "" {
			continue
		}
		lb.appendLocked(line)
	}
	return len(p), nil
}

// Add stores a single line. Useful for tests.
func (lb *LogBuffer) Add(line string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.appendLocked(line)
}

func (lb *LogBuffer) appendLocked(line string) {
	if len(lb.lines) >= lb.capacity {
		lb.lines = lb.lines[1:]
	}
	lb.lines = append(lb.lines, line)
}

// Lines returns the most recent limit lines matching name. If name is empty,
// it returns the most recent limit lines of the entire buffer.
func (lb *LogBuffer) Lines(name string, limit int) []string {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	if limit <= 0 {
		limit = 100
	}

	out := make([]string, 0, limit)
	for i := len(lb.lines) - 1; i >= 0 && len(out) < limit; i-- {
		if name == "" || strings.Contains(lb.lines[i], name) {
			out = append(out, lb.lines[i])
		}
	}
	// Reverse so oldest comes first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// SetupLogFile redirects the standard logger to the given file (append mode).
// The returned file must stay open for the lifetime of the process.
func SetupLogFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	log.SetOutput(f)
	return f, nil
}
