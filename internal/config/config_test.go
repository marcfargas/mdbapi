package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(content)
	_ = f.Close()
	return f.Name()
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("MDBAPI_KEY", strings.Repeat("a", 32))
	path := writeTemp(t, `
auth:
  keys:
    - "${MDBAPI_KEY}"
databases:
  - alias: db1
    path: C:\data\test.mdb
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:8080" {
		t.Errorf("default listen = %q, want 127.0.0.1:8080", cfg.Server.Listen)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("default read timeout = %v, want 30s", cfg.Server.ReadTimeout)
	}
	if cfg.Tunnel.Provider != "none" {
		t.Errorf("default tunnel provider = %q, want none", cfg.Tunnel.Provider)
	}
	if cfg.API.MaxRows != 1000 {
		t.Errorf("default max_rows = %d, want 1000", cfg.API.MaxRows)
	}
}

func TestLoad_EnvSubstitution(t *testing.T) {
	t.Setenv("MDBAPI_KEY", "super-secret-key-that-is-long-enough-for-validation")
	path := writeTemp(t, `
auth:
  keys:
    - "${MDBAPI_KEY}"
databases:
  - alias: db1
    path: C:\data\test.mdb
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Auth.Keys[0] != "super-secret-key-that-is-long-enough-for-validation" {
		t.Errorf("env substitution failed, got %q", cfg.Auth.Keys[0])
	}
}

func TestLoad_UnsetEnvFails(t *testing.T) {
	os.Unsetenv("MDBAPI_KEY_MISSING")
	path := writeTemp(t, `
auth:
  keys:
    - "${MDBAPI_KEY_MISSING}"
databases:
  - alias: db1
    path: C:\data\test.mdb
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for unset env var, got nil")
	}
	if !strings.Contains(err.Error(), "MDBAPI_KEY_MISSING") {
		t.Errorf("error should mention the missing var, got: %v", err)
	}
}

func TestLoad_KeyTooShort(t *testing.T) {
	t.Setenv("SHORT_KEY", "tooshort")
	path := writeTemp(t, `
auth:
  keys:
    - "${SHORT_KEY}"
databases:
  - alias: db1
    path: C:\data\test.mdb
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for short key, got nil")
	}
	if !strings.Contains(err.Error(), "too short") {
		t.Errorf("error should mention key too short, got: %v", err)
	}
}

func TestLoad_NoDatabases(t *testing.T) {
	t.Setenv("MDBAPI_KEY", strings.Repeat("a", 32))
	path := writeTemp(t, `
auth:
  keys:
    - "${MDBAPI_KEY}"
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for missing databases, got nil")
	}
}

func TestLoad_UnknownTunnelProvider(t *testing.T) {
	t.Setenv("MDBAPI_KEY", strings.Repeat("a", 32))
	path := writeTemp(t, `
auth:
  keys:
    - "${MDBAPI_KEY}"
databases:
  - alias: db1
    path: C:\data\test.mdb
tunnel:
  provider: frp
`)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for unknown tunnel provider")
	}
	if !strings.Contains(err.Error(), "frp") {
		t.Errorf("error should mention the invalid provider, got: %v", err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestLoad_MultipleKeys(t *testing.T) {
	t.Setenv("KEY1", strings.Repeat("a", 32))
	t.Setenv("KEY2", strings.Repeat("b", 32))
	path := writeTemp(t, `
auth:
  keys:
    - "${KEY1}"
    - "${KEY2}"
databases:
  - alias: db1
    path: C:\data\test.mdb
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Auth.Keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(cfg.Auth.Keys))
	}
}
