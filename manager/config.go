package manager

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

var DefaultConfigDir = filepath.Join(os.Getenv("HOME"), ".config", "tunnel")
var DefaultDataDir = filepath.Join(os.Getenv("HOME"), ".local", "share", "tunnel")

func init() {
	if os.Getenv("HOME") == "" {
		DefaultConfigDir = "/etc/tunnel"
		DefaultDataDir = "/var/lib/tunnel"
	}
}

type Config struct {
	Tunnels []TunnelConfig `yaml:"tunnels"`
	Global  GlobalConfig   `yaml:"global"`
}

type TunnelConfig struct {
	Name      string   `yaml:"name"`
	Mode      string   `yaml:"mode"`                // local, remote, dynamic
	Local     string   `yaml:"local,omitempty"`     // local addr or target
	Remote    string   `yaml:"remote,omitempty"`    // remote target or server listen port
	Server    string   `yaml:"server,omitempty"`    // server addr (remote client mode)
	Token     string   `yaml:"token,omitempty"`     // auth token
	TLS       bool     `yaml:"tls,omitempty"`       // TLS enabled
	Protocol  string   `yaml:"protocol,omitempty"`  // tcp (default) or udp
	Autostart bool     `yaml:"autostart,omitempty"` // start on daemon launch
	Group     string   `yaml:"group,omitempty"`     // connection group
	Hops      []string `yaml:"hops,omitempty"`      // multi-hop chain
	Connections int    `yaml:"connections,omitempty"` // multi-control connections (remote)
	HealthCheck string `yaml:"health_check,omitempty"` // health check interval (e.g. "10s")
}

type GlobalConfig struct {
	LogLevel      string `yaml:"log_level,omitempty"`
	LogFile       string `yaml:"log_file,omitempty"`
	ControlSocket string `yaml:"control_socket,omitempty"`
	TLSDir        string `yaml:"tls_dir,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		Global: GlobalConfig{
			LogLevel:      "info",
			ControlSocket: filepath.Join(DefaultDataDir, "control.sock"),
			TLSDir:        filepath.Join(DefaultConfigDir, "tls"),
		},
	}
}

func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = filepath.Join(DefaultConfigDir, "config.yaml")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			return &cfg, nil
		}
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func SaveConfig(cfg *Config, path string) error {
	if path == "" {
		path = filepath.Join(DefaultConfigDir, "config.yaml")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
