// Package config handles YAML configuration loading and validation.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration structure.
type Config struct {
	Service   ServiceConfig    `yaml:"service"`
	Server    ServerConfig     `yaml:"server"`
	Auth      AuthConfig       `yaml:"auth"`
	Databases []DatabaseConfig `yaml:"databases"`
	Tunnel    TunnelConfig     `yaml:"tunnel"`
	Updater   UpdaterConfig    `yaml:"updater"`
	API       APIConfig        `yaml:"api"`
}

type ServiceConfig struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"display_name"`
	Description string `yaml:"description"`
	LogFile     string `yaml:"log_file"`
}

type ServerConfig struct {
	Listen       string        `yaml:"listen"`
	ReadTimeout  time.Duration `yaml:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout"`
	MaxBodySize  int64         `yaml:"max_body_size"`
	TLSCert      string        `yaml:"tls_cert"`
	TLSKey       string        `yaml:"tls_key"`
}

type AuthConfig struct {
	Keys []string `yaml:"keys"`
}

type DatabaseConfig struct {
	Alias string `yaml:"alias"`
	Path  string `yaml:"path"`
}

type TunnelConfig struct {
	Provider string      `yaml:"provider"` // none | tsnet
	Tsnet    TsnetConfig `yaml:"tsnet"`
}

type TsnetConfig struct {
	Hostname string `yaml:"hostname"`
	AuthKey  string `yaml:"auth_key"`
	StateDir string `yaml:"state_dir"`
	Funnel   bool   `yaml:"funnel"`
}

type UpdaterConfig struct {
	Enabled       bool          `yaml:"enabled"`
	GithubRepo    string        `yaml:"github_repo"`
	CheckInterval time.Duration `yaml:"check_interval"`
	GithubToken   string        `yaml:"github_token"`
}

type APIConfig struct {
	MaxRows int `yaml:"max_rows"`
}

// Load reads and parses a YAML config file, substituting environment variables.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	expanded := expandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// expandEnv replaces ${VAR} and $VAR occurrences with environment values.
// Fails loudly if a referenced variable is unset.
func expandEnv(s string) string {
	re := regexp.MustCompile(`\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)
	return re.ReplaceAllStringFunc(s, func(match string) string {
		var name string
		if strings.HasPrefix(match, "${") {
			name = match[2 : len(match)-1]
		} else {
			name = match[1:]
		}
		val, ok := os.LookupEnv(name)
		if !ok {
			// Return a sentinel that validation will catch.
			return fmt.Sprintf("__UNSET_ENV_%s__", name)
		}
		return val
	})
}

func applyDefaults(cfg *Config) {
	if cfg.Service.Name == "" {
		cfg.Service.Name = "MDBRestService"
	}
	if cfg.Service.DisplayName == "" {
		cfg.Service.DisplayName = "MDB REST API Service"
	}
	if cfg.Service.Description == "" {
		cfg.Service.Description = "Exposes Access MDB/ACCDB files via a read-only REST API"
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:8080"
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 30 * time.Second
	}
	if cfg.Server.MaxBodySize == 0 {
		cfg.Server.MaxBodySize = 1 << 20 // 1 MB
	}
	if cfg.Tunnel.Provider == "" {
		cfg.Tunnel.Provider = "none"
	}
	if cfg.Updater.CheckInterval == 0 {
		cfg.Updater.CheckInterval = time.Hour
	}
	if cfg.API.MaxRows == 0 {
		cfg.API.MaxRows = 1000
	}
	if cfg.Service.LogFile == "" {
		cfg.Service.LogFile = `C:\ProgramData\MDBService\mdbapi.log`
	}
}

const minKeyLen = 32

var unsetEnvRe = regexp.MustCompile(`__UNSET_ENV_([^_]+)__`)

func validate(cfg *Config) error {
	var errs []string

	// Check for unresolved env vars in auth keys.
	for i, k := range cfg.Auth.Keys {
		if m := unsetEnvRe.FindStringSubmatch(k); m != nil {
			errs = append(errs, fmt.Sprintf("auth.keys[%d]: environment variable %q is not set", i, m[1]))
		} else if len(k) < minKeyLen {
			errs = append(errs, fmt.Sprintf("auth.keys[%d]: key too short (min %d chars), use 'mdbapi keygen'", i, minKeyLen))
		}
	}

	// At least one database required.
	if len(cfg.Databases) == 0 {
		errs = append(errs, "databases: at least one database alias must be configured")
	}
	for i, db := range cfg.Databases {
		if db.Alias == "" {
			errs = append(errs, fmt.Sprintf("databases[%d]: alias is required", i))
		}
		if db.Path == "" {
			errs = append(errs, fmt.Sprintf("databases[%d]: path is required", i))
		}
	}

	// Tunnel provider must be known.
	switch cfg.Tunnel.Provider {
	case "none", "tsnet":
	default:
		errs = append(errs, fmt.Sprintf("tunnel.provider %q is not supported (valid: none, tsnet)", cfg.Tunnel.Provider))
	}

	if len(errs) > 0 {
		return errors.New("config validation failed:\n  - " + strings.Join(errs, "\n  - "))
	}
	return nil
}
