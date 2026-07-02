package manager

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func GenerateSystemdUnit() string {
	binPath, _ := exec.LookPath(os.Args[0])
	if binPath == "" {
		binPath = "/usr/local/bin/tunnel"
	}
	configPath := filepath.Join(DefaultConfigDir, "config.yaml")
	user := os.Getenv("USER")
	if user == "" {
		user = "root"
	}

	return fmt.Sprintf(`[Unit]
Description=Tunnel - port forwarding daemon
After=network.target

[Service]
Type=simple
User=%s
ExecStart=%s start --foreground
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5
Environment=TUNNEL_CONFIG=%s

[Install]
WantedBy=multi-user.target
`, user, binPath, configPath)
}

func WriteSystemdUnit(path string) error {
	content := GenerateSystemdUnit()
	if path == "" {
		path = "tunnel.service"
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func WriteSystemdUnitUser(path string) error {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home dir: %w", err)
		}
		path = filepath.Join(home, ".config", "systemd", "user", "tunnel.service")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}
	return WriteSystemdUnit(path)
}

func SystemdInstallHint() string {
	binPath, _ := exec.LookPath(os.Args[0])
	if binPath == "" {
		binPath = "/usr/local/bin/tunnel"
	}
	hint := fmt.Sprintf(`# Generate systemd unit:
  tunnel init --systemd > /etc/systemd/system/tunnel.service
  systemctl daemon-reload
  systemctl enable --now tunnel

# Or for user mode:
  mkdir -p ~/.config/systemd/user
  tunnel init --systemd-user > ~/.config/systemd/user/tunnel.service
  systemctl --user daemon-reload
  systemctl --user enable --now tunnel
`)
	return strings.TrimSpace(hint)
}
