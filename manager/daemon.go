package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func PidfilePath() string {
	return filepath.Join(DefaultDataDir, "tunneld.pid")
}

func WritePidfile() error {
	path := PidfilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0644)
}

func RemovePidfile() error {
	return os.Remove(PidfilePath())
}

func ReadPidfile() (int, error) {
	data, err := os.ReadFile(PidfilePath())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid: %w", err)
	}
	return pid, nil
}

func IsRunning() bool {
	pid, err := ReadPidfile()
	if err != nil {
		return false
	}
	return processExists(pid)
}

func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
