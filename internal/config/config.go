// Package config handles YAML configuration loading and validation.
package config

import (
	"errors"
	"fmt"
	"net"
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

	// GlobEntries holds the original glob-type database entries before expansion.
	// Used by the file watcher to detect new databases at runtime.
	GlobEntries []DatabaseConfig `yaml:"-"`
}

type ServiceConfig struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"display_name"`
	Description string `yaml:"description"`
	LogFile     string `yaml:"log_file"`
	LogMaxSize  int    `yaml:"log_max_size"`  // max size in MB before rotation
	LogMaxFiles int    `yaml:"log_max_files"` // max number of rotated files to keep
	LogMaxAge   int    `yaml:"log_max_age"`   // max age in days before deletion
	LogCompress bool   `yaml:"log_compress"`  // gzip rotated files
}

type ServerConfig struct {
	Listen         string        `yaml:"listen"`
	ReadTimeout    time.Duration `yaml:"read_timeout"`
	WriteTimeout   time.Duration `yaml:"write_timeout"`
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
	MaxBodySize    int64         `yaml:"max_body_size"`
	MaxHeaderBytes int           `yaml:"max_header_bytes"`
	RateLimit      float64       `yaml:"rate_limit"`  // requests per second per IP (0 = unlimited)
	RateBurst      int           `yaml:"rate_burst"`   // burst capacity per IP
	TLSCert        string        `yaml:"tls_cert"`
	TLSKey         string        `yaml:"tls_key"`
}

type AuthConfig struct {
	Keys       []string `yaml:"keys"`
	AllowedIPs []string `yaml:"allowed_ips"` // CIDRs or IPs; empty = allow all
}

// DatabaseConfig is either an explicit entry (Alias+Path) or a glob pattern.
// Exactly one form must be used; mixing fields is a validation error.
//
//	# Explicit
//	- alias: inventory
//	  path:  'C:\Data\Inventory.mdb'
//
//	# Single-level glob — all *.mdb directly in C:\Data
//	- glob: 'C:\Data\*.mdb'
//
//	# Recursive glob — all *.mdb anywhere under C:\Data
//	- glob: 'C:\Data\**\*.mdb'
type DatabaseConfig struct {
	Alias  string `yaml:"alias"`
	Path   string `yaml:"path"`
	Glob   string `yaml:"glob"`
	Driver string `yaml:"driver,omitempty"` // optional ODBC driver override
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
	Channel       string        `yaml:"channel"` // "release" (default) or "develop"
	Variant       string        `yaml:"variant"` // "standard" (default) or "tsnet"
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
	// Preserve original glob entries for the watcher before expansion replaces them.
	for _, db := range cfg.Databases {
		if db.Glob != "" {
			cfg.GlobEntries = append(cfg.GlobEntries, db)
		}
	}

	// Expand glob entries into concrete Alias+Path entries.
	// This happens after structural validation so glob syntax errors are
	// reported clearly, separate from structural config errors.
	resolved, err := expandGlobs(cfg.Databases)
	if err != nil {
		return nil, fmt.Errorf("config: expand globs: %w", err)
	}
	cfg.Databases = resolved
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
	if cfg.Server.MaxHeaderBytes == 0 {
		cfg.Server.MaxHeaderBytes = 8 << 10 // 8 KB
	}
	if cfg.Server.IdleTimeout == 0 {
		cfg.Server.IdleTimeout = 120 * time.Second
	}
	if cfg.Server.RequestTimeout == 0 {
		cfg.Server.RequestTimeout = 30 * time.Second
	}
	if cfg.Server.RateLimit == 0 {
		cfg.Server.RateLimit = 10 // 10 req/s per IP
	}
	if cfg.Server.RateBurst == 0 {
		cfg.Server.RateBurst = 20
	}
	if cfg.Tunnel.Provider == "" {
		cfg.Tunnel.Provider = "none"
	}
	if cfg.Updater.CheckInterval == 0 {
		cfg.Updater.CheckInterval = time.Hour
	}
	if cfg.Updater.Channel == "" {
		cfg.Updater.Channel = "release"
	}
	if cfg.Updater.Variant == "" {
		cfg.Updater.Variant = "standard"
	}
	if cfg.API.MaxRows == 0 {
		cfg.API.MaxRows = 1000
	}
	if cfg.Service.LogFile == "" {
		cfg.Service.LogFile = `C:\ProgramData\MDBService\mdbapi.log`
	}
	if cfg.Service.LogMaxSize == 0 {
		cfg.Service.LogMaxSize = 5 // MB
	}
	if cfg.Service.LogMaxFiles == 0 {
		cfg.Service.LogMaxFiles = 5
	}
	if cfg.Service.LogMaxAge == 0 {
		cfg.Service.LogMaxAge = 30 // days
	}
}

const minKeyLen = 32

func validate(cfg *Config) error {
	var errs []string

	// Check for unresolved env vars in auth keys.
	// Sentinel format: __UNSET_ENV_VARNAME__ (VARNAME may contain underscores).
	for i, k := range cfg.Auth.Keys {
		if strings.HasPrefix(k, "__UNSET_ENV_") && strings.HasSuffix(k, "__") {
			varName := strings.TrimPrefix(k, "__UNSET_ENV_")
			varName = strings.TrimSuffix(varName, "__")
			errs = append(errs, fmt.Sprintf("auth.keys[%d]: environment variable %q is not set", i, varName))
		} else if len(k) < minKeyLen {
			errs = append(errs, fmt.Sprintf("auth.keys[%d]: key too short (min %d chars), use 'mdbapi keygen'", i, minKeyLen))
		}
	}

	// At least one database entry required (explicit or glob).
	if len(cfg.Databases) == 0 {
		errs = append(errs, "databases: at least one database entry must be configured")
	}
	for i, db := range cfg.Databases {
		if db.Glob != "" {
			// Glob entry: alias and path must not be set.
			if db.Alias != "" {
				errs = append(errs, fmt.Sprintf("databases[%d]: cannot set both 'glob' and 'alias'", i))
			}
			if db.Path != "" {
				errs = append(errs, fmt.Sprintf("databases[%d]: cannot set both 'glob' and 'path'", i))
			}
		} else {
			// Explicit entry: alias and path are required.
			if db.Alias == "" {
				errs = append(errs, fmt.Sprintf("databases[%d]: alias is required", i))
			}
			if db.Path == "" {
				errs = append(errs, fmt.Sprintf("databases[%d]: path is required", i))
			}
		}
	}

	// Validate allowed_ips as valid IPs or CIDRs.
	for i, entry := range cfg.Auth.AllowedIPs {
		if strings.Contains(entry, "/") {
			if _, _, err := net.ParseCIDR(entry); err != nil {
				errs = append(errs, fmt.Sprintf("auth.allowed_ips[%d]: invalid CIDR %q: %v", i, entry, err))
			}
		} else {
			if net.ParseIP(entry) == nil {
				errs = append(errs, fmt.Sprintf("auth.allowed_ips[%d]: invalid IP %q", i, entry))
			}
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
