package manager

import (
	"fmt"
	"log"
	"os"
)

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
