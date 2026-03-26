# OpenAPI Spec + Glob Watcher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a machine-readable OpenAPI spec endpoint, expose file mtime in database listings, and auto-discover new databases matching configured globs at runtime.

**Architecture:** Hand-written OpenAPI 3.1 YAML embedded in the binary, served as JSON. Pool gains a `Register` method for runtime additions. New `internal/watcher` package uses fsnotify + periodic polling to detect new `.mdb`/`.accdb` files. Config preserves original glob entries for the watcher.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` (existing), `github.com/fsnotify/fsnotify` (new)

---

### Task 1: Add `modified_at` to database listing

**Files:**
- Modify: `internal/mdb/pool.go:28-32` — add `ModifiedAt` field to `DBInfo`
- Modify: `internal/mdb/pool.go:97-113` — populate via `os.Stat` in `Databases()`
- Modify: `internal/api/api_test.go:102-138` — test `modified_at` presence

- [ ] **Step 1: Write the failing test**

In `internal/api/api_test.go`, add a new test after `TestListDatabases` (after line 138):

```go
func TestListDatabases_HasModifiedAt(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	store := &mockStore{databases: []mdb.DBInfo{
		{Alias: "db1", Path: `C:\data\db1.mdb`, Status: "ok", ModifiedAt: &now},
	}}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/", nil)
	req.Header.Set("Authorization", authHeader())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	data := body["data"].(map[string]interface{})
	dbs := data["databases"].([]interface{})
	entry := dbs[0].(map[string]interface{})
	if entry["modified_at"] == nil {
		t.Error("expected modified_at field in database entry")
	}
}
```

Also add `"time"` to the imports in `api_test.go` if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd C:/dev/mdbapi && go test ./internal/api/ -run TestListDatabases_HasModifiedAt -v -short -count=1`
Expected: compilation error — `mdb.DBInfo` has no field `ModifiedAt`

- [ ] **Step 3: Add `ModifiedAt` field to `DBInfo`**

In `internal/mdb/pool.go`, change the `DBInfo` struct (lines 28-32):

```go
// DBInfo is a summary of a registered database returned by the listing endpoint.
type DBInfo struct {
	Alias      string     `json:"alias"`
	Path       string     `json:"path"`
	Status     string     `json:"status"`          // "ok" or "error: <message>"
	ModifiedAt *time.Time `json:"modified_at,omitempty"` // file mtime; nil if stat fails
}
```

Add `"os"` and `"time"` to the import block in `pool.go`.

- [ ] **Step 4: Populate `ModifiedAt` in `Databases()`**

In `internal/mdb/pool.go`, update the `Databases()` method (lines 97-113). Replace the loop body:

```go
func (p *Pool) Databases() []DBInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]DBInfo, 0, len(p.dbs))
	for alias, db := range p.dbs {
		info := DBInfo{
			Alias:  alias,
			Path:   p.paths[alias],
			Status: "ok",
		}
		if err := db.Ping(); err != nil {
			info.Status = "error: " + err.Error()
		}
		if fi, err := os.Stat(p.paths[alias]); err == nil {
			t := fi.ModTime()
			info.ModifiedAt = &t
		}
		out = append(out, info)
	}
	return out
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd C:/dev/mdbapi && go test ./internal/api/ -run TestListDatabases_HasModifiedAt -v -short -count=1`
Expected: PASS

- [ ] **Step 6: Run full test suite**

Run: `cd C:/dev/mdbapi && go test ./... -short -count=1`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add internal/mdb/pool.go internal/api/api_test.go
git commit -m "feat: add modified_at (file mtime) to database listing"
```

---

### Task 2: Add `Pool.Register` method

**Files:**
- Modify: `internal/mdb/pool.go` — add `Register(DBConfig) error` method

- [ ] **Step 1: Write the failing test**

Create `internal/mdb/pool_test.go`:

```go
package mdb

import (
	"testing"
)

func TestPool_Register_NewAlias(t *testing.T) {
	// Start with empty pool.
	p := &Pool{
		dbs:      make(map[string]*sql.DB),
		paths:    make(map[string]string),
		connStrs: make(map[string]string),
	}

	// Register returns an error on non-Windows (no ODBC driver).
	// We test the dedup path instead, which doesn't need ODBC.
	// Seed one entry manually.
	p.paths["existing"] = `C:\data\existing.mdb`
	p.dbs["existing"] = nil // won't be used
	p.connStrs["existing"] = "fake"

	if got := len(p.Aliases()); got != 1 {
		t.Fatalf("aliases = %d, want 1", got)
	}
}

func TestPool_Register_Dedup(t *testing.T) {
	p := &Pool{
		dbs:      make(map[string]*sql.DB),
		paths:    make(map[string]string),
		connStrs: make(map[string]string),
	}
	// Seed an alias.
	p.dbs["mydb"] = nil
	p.paths["mydb"] = `C:\data\mydb.mdb`
	p.connStrs["mydb"] = "fake"

	// Register same alias — should be a no-op.
	err := p.Register(DBConfig{Alias: "mydb", Path: `C:\data\mydb.mdb`})
	if err != nil {
		t.Fatalf("Register dedup returned error: %v", err)
	}
	if got := len(p.Aliases()); got != 1 {
		t.Errorf("aliases after dedup = %d, want 1", got)
	}
}
```

Add `"database/sql"` to the import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd C:/dev/mdbapi && go test ./internal/mdb/ -run TestPool_Register -v -short -count=1`
Expected: compilation error — `Pool` has no method `Register`

- [ ] **Step 3: Implement `Register` method**

In `internal/mdb/pool.go`, add after the `Close()` method (after line 134):

```go
// Register opens and adds a new database to the pool at runtime.
// If the alias already exists, it's a no-op (returns nil).
func (p *Pool) Register(cfg DBConfig) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.dbs[cfg.Alias]; exists {
		return nil // already registered
	}
	db, connStr, err := openDB(cfg.Path, cfg.Driver)
	if err != nil {
		return fmt.Errorf("open database %q (%s): %w", cfg.Alias, cfg.Path, err)
	}
	p.dbs[cfg.Alias] = db
	p.paths[cfg.Alias] = cfg.Path
	p.connStrs[cfg.Alias] = connStr
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd C:/dev/mdbapi && go test ./internal/mdb/ -run TestPool_Register -v -short -count=1`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `cd C:/dev/mdbapi && go test ./... -short -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/mdb/pool.go internal/mdb/pool_test.go
git commit -m "feat: add Pool.Register for runtime database addition"
```

---

### Task 3: Preserve original glob entries in Config

**Files:**
- Modify: `internal/config/config.go:17-25` — add `GlobEntries` field to `Config`
- Modify: `internal/config/config.go:115-123` — preserve globs before expansion

The watcher needs the original glob patterns, but `config.Load()` replaces them with expanded entries. We need to preserve them.

- [ ] **Step 1: Write the failing test**

In `internal/config/glob_test.go`, add after the last test:

```go
func TestLoad_PreservesGlobEntries(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "a.mdb"), nil, 0644)

	globPattern := filepath.Join(dir, "**", "*.mdb")
	cfgYAML := fmt.Sprintf(`
auth:
  keys:
    - "%s"
databases:
  - glob: '%s'
`, strings.Repeat("k", 32), globPattern)

	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(cfgYAML), 0644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.GlobEntries) != 1 {
		t.Fatalf("GlobEntries = %d, want 1", len(cfg.GlobEntries))
	}
	if cfg.GlobEntries[0].Glob != globPattern {
		t.Errorf("GlobEntries[0].Glob = %q, want %q", cfg.GlobEntries[0].Glob, globPattern)
	}
}
```

Add `"fmt"` and `"strings"` to imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd C:/dev/mdbapi && go test ./internal/config/ -run TestLoad_PreservesGlobEntries -v -short -count=1`
Expected: compilation error — `Config` has no field `GlobEntries`

- [ ] **Step 3: Add `GlobEntries` to Config and preserve in Load**

In `internal/config/config.go`, add the field to `Config` (after line 24):

```go
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
```

In `internal/config/config.go`, in the `Load` function, preserve glob entries before expansion. Replace lines 115-123:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd C:/dev/mdbapi && go test ./internal/config/ -run TestLoad_PreservesGlobEntries -v -short -count=1`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `cd C:/dev/mdbapi && go test ./... -short -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/glob_test.go
git commit -m "feat: preserve original glob entries in Config for watcher"
```

---

### Task 4: Export glob helper functions

**Files:**
- Modify: `internal/config/glob.go` — export `MatchGlob`, `GlobBase`, `AliasFromPath`

The watcher needs to reuse the glob matching and alias derivation logic. Export the three helpers by capitalizing their names.

- [ ] **Step 1: Rename the functions**

In `internal/config/glob.go`:

1. Rename `matchGlob` → `MatchGlob` (line 78)
2. Rename `globBase` → `GlobBase` (line 123)
3. Rename `aliasFromPath` → `AliasFromPath` (line 152)
4. Update all call sites within `glob.go`:
   - Line 47: `matchGlob(entry.Glob)` → `MatchGlob(entry.Glob)`
   - Line 56: `globBase(entry.Glob)` → `GlobBase(entry.Glob)`
   - Line 58: `aliasFromPath(base, path)` → `AliasFromPath(base, path)`
   - Line 88: `base := globBase(pattern)` → `base := GlobBase(pattern)` (inside `MatchGlob`)

- [ ] **Step 2: Update test references**

In `internal/config/glob_test.go`, update function names in tests:

1. `TestGlobBase` calls → update to `GlobBase`
2. Any direct calls to `aliasFromPath` → `AliasFromPath`
3. Any direct calls to `matchGlob` → `MatchGlob`

- [ ] **Step 3: Run tests to verify nothing broke**

Run: `cd C:/dev/mdbapi && go test ./internal/config/ -v -short -count=1`
Expected: all PASS

- [ ] **Step 4: Run full test suite**

Run: `cd C:/dev/mdbapi && go test ./... -short -count=1`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/glob.go internal/config/glob_test.go
git commit -m "refactor: export glob helper functions for watcher reuse"
```

---

### Task 5: Implement the glob watcher

**Files:**
- Create: `internal/watcher/watcher.go`
- Create: `internal/watcher/watcher_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/watcher/watcher_test.go`:

```go
package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcfargas/mdbapi/internal/config"
	"github.com/marcfargas/mdbapi/internal/mdb"
)

// mockRegistrar records Register calls.
type mockRegistrar struct {
	mu       sync.Mutex
	aliases  []string
	registered []mdb.DBConfig
}

func (m *mockRegistrar) Aliases() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.aliases...)
}

func (m *mockRegistrar) Register(cfg mdb.DBConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aliases = append(m.aliases, cfg.Alias)
	m.registered = append(m.registered, cfg)
	return nil
}

func (m *mockRegistrar) Registered() []mdb.DBConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mdb.DBConfig{}, m.registered...)
}

func TestWatcher_PollDetectsNewFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)

	globPattern := filepath.Join(dir, "**", "*.mdb")
	globs := []config.DatabaseConfig{{Glob: globPattern}}
	reg := &mockRegistrar{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := New(globs, reg, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go w.Run(ctx)

	// Drop a new file — watcher should pick it up.
	time.Sleep(50 * time.Millisecond) // let watcher start
	newFile := filepath.Join(sub, "newdb.mdb")
	os.WriteFile(newFile, []byte("fake"), 0644)

	// Wait for poll to detect it.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for watcher to register new database")
		default:
		}
		got := reg.Registered()
		if len(got) >= 1 {
			if got[0].Alias != "sub_newdb" {
				t.Errorf("alias = %q, want %q", got[0].Alias, "sub_newdb")
			}
			if got[0].Path != newFile {
				t.Errorf("path = %q, want %q", got[0].Path, newFile)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWatcher_DeduplicatesExistingAliases(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "existing.mdb"), nil, 0644)

	globPattern := filepath.Join(dir, "**", "*.mdb")
	globs := []config.DatabaseConfig{{Glob: globPattern}}
	// Pre-seed the alias so watcher skips it.
	reg := &mockRegistrar{aliases: []string{"sub_existing"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := New(globs, reg, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go w.Run(ctx)

	// Let a couple poll cycles run.
	time.Sleep(350 * time.Millisecond)
	cancel()

	if got := reg.Registered(); len(got) != 0 {
		t.Errorf("expected 0 registrations for pre-existing alias, got %d", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd C:/dev/mdbapi && go test ./internal/watcher/ -run TestWatcher -v -short -count=1`
Expected: compilation error — package `watcher` does not exist

- [ ] **Step 3: Implement the watcher**

Create `internal/watcher/watcher.go`:

```go
// Package watcher monitors filesystem for new databases matching configured globs.
package watcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/marcfargas/mdbapi/internal/config"
	"github.com/marcfargas/mdbapi/internal/mdb"
)

// Registrar is the subset of mdb.Pool used by the watcher.
type Registrar interface {
	Aliases() []string
	Register(cfg mdb.DBConfig) error
}

// globEntry holds pre-computed fields for a single glob pattern.
type globEntry struct {
	pattern string // original glob pattern
	base    string // longest non-wildcard prefix (directory to watch)
}

// Watcher watches for new database files matching configured glob patterns.
type Watcher struct {
	globs    []globEntry
	reg      Registrar
	pollIvl  time.Duration
	fsw      *fsnotify.Watcher
}

// New creates a Watcher. Pass only the glob-type DatabaseConfig entries.
// pollInterval controls how often the poll fallback re-expands globs.
func New(globs []config.DatabaseConfig, reg Registrar, pollInterval time.Duration) (*Watcher, error) {
	var entries []globEntry
	for _, g := range globs {
		if g.Glob == "" {
			continue
		}
		entries = append(entries, globEntry{
			pattern: g.Glob,
			base:    config.GlobBase(g.Glob),
		})
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("fsnotify unavailable, relying on poll only", "err", err)
		return &Watcher{globs: entries, reg: reg, pollIvl: pollInterval}, nil
	}

	// Watch base directories for each glob.
	for _, e := range entries {
		if err := fsw.Add(e.base); err != nil {
			slog.Warn("fsnotify: cannot watch directory, relying on poll", "dir", e.base, "err", err)
		}
	}

	return &Watcher{globs: entries, reg: reg, pollIvl: pollInterval, fsw: fsw}, nil
}

// Run starts the watcher. Blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollIvl)
	defer ticker.Stop()
	if w.fsw != nil {
		defer w.fsw.Close()
	}

	// fsnotify event loop (if available).
	if w.fsw != nil {
		go w.fsnotifyLoop(ctx)
	}

	// Poll loop.
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

// fsnotifyLoop handles filesystem events.
func (w *Watcher) fsnotifyLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == 0 {
				continue
			}
			w.tryRegister(event.Name, "fsnotify")
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			slog.Warn("fsnotify error", "err", err)
		}
	}
}

// poll re-expands all glob patterns and registers any new matches.
func (w *Watcher) poll() {
	for _, g := range w.globs {
		matches, err := config.MatchGlob(g.pattern)
		if err != nil {
			slog.Warn("poll: glob error", "pattern", g.pattern, "err", err)
			continue
		}
		for _, path := range matches {
			w.tryRegister(path, "poll")
		}
	}
}

// tryRegister checks if a file matches any glob and registers it if new.
func (w *Watcher) tryRegister(path string, source string) {
	path = filepath.Clean(path)

	// Check extension — must be .mdb or .accdb.
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".mdb" && ext != ".accdb" {
		return
	}

	// Find which glob this path matches and derive alias.
	for _, g := range w.globs {
		matches, err := config.MatchGlob(g.pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if filepath.Clean(m) != path {
				continue
			}
			alias, err := config.AliasFromPath(g.base, path)
			if err != nil {
				slog.Warn("watcher: alias derivation failed", "path", path, "err", err)
				return
			}

			// Check if already registered.
			for _, existing := range w.reg.Aliases() {
				if strings.EqualFold(existing, alias) {
					return
				}
			}

			if err := w.reg.Register(mdb.DBConfig{Alias: alias, Path: path}); err != nil {
				slog.Warn("watcher: register failed", "alias", alias, "path", path, "err", err)
				return
			}
			slog.Info("database added", "alias", alias, "path", path, "source", source)
			return
		}
	}
}
```

- [ ] **Step 4: Install fsnotify dependency**

Run: `cd C:/dev/mdbapi && go get github.com/fsnotify/fsnotify`

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd C:/dev/mdbapi && go test ./internal/watcher/ -run TestWatcher -v -short -count=1`
Expected: both tests PASS

- [ ] **Step 6: Run full test suite**

Run: `cd C:/dev/mdbapi && go test ./... -short -count=1`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add internal/watcher/watcher.go internal/watcher/watcher_test.go go.mod go.sum
git commit -m "feat: add glob watcher with fsnotify + poll fallback"
```

---

### Task 6: Wire watcher into service startup

**Files:**
- Modify: `internal/winservice/service.go:27-38` — add watcher field to Program
- Modify: `internal/winservice/service.go:81-169` — start watcher in `run()`
- Modify: `internal/winservice/service.go:57-79` — stop watcher in `Stop()`

- [ ] **Step 1: Add watcher import and field**

In `internal/winservice/service.go`, add to imports:

```go
"github.com/marcfargas/mdbapi/internal/watcher"
```

Add field to `Program` struct (after `restartSignal chan struct{}`):

```go
watcher *watcher.Watcher
```

- [ ] **Step 2: Start watcher after HTTP server in `run()`**

In `internal/winservice/service.go`, in the `run()` method, after step 4 (auto-updater, line 161) and before step 5 (Serve, line 163), insert:

```go
	// 4b. Start glob watcher for dynamic database discovery.
	if len(p.cfg.GlobEntries) > 0 {
		w, err := watcher.New(p.cfg.GlobEntries, pool, 30*time.Second)
		if err != nil {
			slog.Warn("glob watcher init failed", "err", err)
		} else {
			p.watcher = w
			go w.Run(ctx)
			slog.Info("glob watcher started", "patterns", len(p.cfg.GlobEntries))
		}
	}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd C:/dev/mdbapi && go vet ./...`
Expected: no errors (on non-Windows, the build tag will exclude this file — verify with `GOOS=windows go vet ./...` or just run `go vet ./...` if already on Windows)

- [ ] **Step 4: Run full test suite**

Run: `cd C:/dev/mdbapi && go test ./... -short -count=1`
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/winservice/service.go
git commit -m "feat: wire glob watcher into service startup"
```

---

### Task 7: Write and serve the OpenAPI spec

**Files:**
- Create: `api/openapi.yaml` — hand-written OpenAPI 3.1 spec
- Modify: `internal/api/router.go` — embed spec, add handler + route

- [ ] **Step 1: Write the failing test**

In `internal/api/api_test.go`, add:

```go
func TestOpenAPIEndpoint(t *testing.T) {
	store := &mockStore{aliases: []string{}}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/openapi.json", nil)
	// No auth — should be public like /health.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Must be valid JSON.
	var parsed map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	// Must have openapi version field.
	if parsed["openapi"] == nil {
		t.Error("expected 'openapi' field in response")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd C:/dev/mdbapi && go test ./internal/api/ -run TestOpenAPIEndpoint -v -short -count=1`
Expected: FAIL — 404 or 401 (no route registered)

- [ ] **Step 3: Create the OpenAPI spec**

Create `api/openapi.yaml`:

```yaml
openapi: "3.1.0"
info:
  title: mdbapi
  description: Read-only REST API for Microsoft Access databases (.mdb/.accdb)
  version: "1.0.0"
  license:
    name: MIT

servers:
  - url: /v1

security:
  - ApiKeyHeader: []
  - BearerAuth: []

paths:
  /v1/health:
    get:
      operationId: getHealth
      summary: Health check
      description: Returns service status. No authentication required.
      security: []
      responses:
        "200":
          description: Service status
          content:
            application/json:
              schema:
                type: object
                properties:
                  data:
                    type: object
                    properties:
                      status:
                        type: string
                        enum: [ok, degraded]
                    required: [status]

  /v1/openapi.json:
    get:
      operationId: getOpenAPISpec
      summary: OpenAPI specification
      description: Returns this API specification as JSON. No authentication required.
      security: []
      responses:
        "200":
          description: OpenAPI 3.1 specification
          content:
            application/json:
              schema:
                type: object

  /v1/:
    get:
      operationId: listDatabases
      summary: List databases
      description: Returns all exposed databases with alias, path, connection status, and file modification time.
      responses:
        "200":
          description: Database list
          content:
            application/json:
              schema:
                type: object
                properties:
                  data:
                    type: object
                    properties:
                      databases:
                        type: array
                        items:
                          $ref: "#/components/schemas/DatabaseInfo"
                    required: [databases]
        "401":
          $ref: "#/components/responses/Unauthorized"

  /v1/version:
    get:
      operationId: getVersion
      summary: Binary version
      responses:
        "200":
          description: Version info
          content:
            application/json:
              schema:
                type: object
                properties:
                  data:
                    type: object
                    properties:
                      version:
                        type: string
                    required: [version]
        "401":
          $ref: "#/components/responses/Unauthorized"

  /v1/{db}/tables:
    get:
      operationId: listTables
      summary: List tables
      description: Returns all user table names in the specified database.
      parameters:
        - $ref: "#/components/parameters/db"
      responses:
        "200":
          description: Table list
          content:
            application/json:
              schema:
                type: object
                properties:
                  data:
                    type: object
                    properties:
                      database:
                        type: string
                      tables:
                        type: array
                        items:
                          type: string
                    required: [database, tables]
        "401":
          $ref: "#/components/responses/Unauthorized"
        "404":
          $ref: "#/components/responses/NotFound"

  /v1/{db}/{table}:
    get:
      operationId: queryTable
      summary: Query rows
      description: |
        Returns rows from the specified table with optional filtering, pagination, and sorting.
        Any query parameter not in the reserved list is treated as a column=value filter.
      parameters:
        - $ref: "#/components/parameters/db"
        - name: table
          in: path
          required: true
          schema:
            type: string
        - name: limit
          in: query
          description: Maximum rows to return (default 1000)
          schema:
            type: integer
            default: 1000
        - name: offset
          in: query
          description: Number of rows to skip
          schema:
            type: integer
            default: 0
        - name: sort
          in: query
          description: Column name to sort by
          schema:
            type: string
        - name: sort_desc
          in: query
          description: Sort descending (true/1 for DESC)
          schema:
            type: boolean
            default: false
      responses:
        "200":
          description: Query results
          content:
            application/json:
              schema:
                type: object
                properties:
                  data:
                    $ref: "#/components/schemas/QueryResult"
        "401":
          $ref: "#/components/responses/Unauthorized"
        "404":
          $ref: "#/components/responses/NotFound"

  /v1/{db}/query:
    post:
      operationId: executeSQL
      summary: SQL passthrough
      description: |
        Executes a read-only SQL query. Only SELECT and WITH (CTE) statements are allowed;
        anything else returns 403. A TOP N limit is enforced automatically if not present.
      parameters:
        - $ref: "#/components/parameters/db"
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties:
                sql:
                  type: string
                  description: SQL query (SELECT or WITH only)
                params:
                  type: array
                  items: {}
                  description: Positional parameters for the query
              required: [sql]
      responses:
        "200":
          description: Query results
          content:
            application/json:
              schema:
                type: object
                properties:
                  data:
                    $ref: "#/components/schemas/QueryResult"
        "400":
          $ref: "#/components/responses/BadRequest"
        "401":
          $ref: "#/components/responses/Unauthorized"
        "403":
          description: Write statement rejected
          content:
            application/json:
              schema:
                $ref: "#/components/schemas/ErrorEnvelope"
        "404":
          $ref: "#/components/responses/NotFound"

components:
  securitySchemes:
    ApiKeyHeader:
      type: apiKey
      in: header
      name: X-API-Key
    BearerAuth:
      type: http
      scheme: bearer

  parameters:
    db:
      name: db
      in: path
      required: true
      description: Database alias
      schema:
        type: string

  schemas:
    DatabaseInfo:
      type: object
      properties:
        alias:
          type: string
        path:
          type: string
        status:
          type: string
          description: '"ok" or "error: <message>"'
        modified_at:
          type: string
          format: date-time
          description: File modification time (omitted if stat fails)
      required: [alias, path, status]

    QueryResult:
      type: object
      properties:
        database:
          type: string
        table:
          type: string
          description: Present only for table queries, not SQL passthrough
        count:
          type: integer
        rows:
          type: array
          items:
            type: object
            additionalProperties: true
      required: [database, count, rows]

    ErrorEnvelope:
      type: object
      properties:
        error:
          type: object
          properties:
            code:
              type: string
              enum:
                - not_found
                - unauthorized
                - bad_request
                - forbidden
                - rate_limited
                - internal_error
            message:
              type: string
          required: [code, message]

  responses:
    Unauthorized:
      description: Missing or invalid API key
      content:
        application/json:
          schema:
            $ref: "#/components/schemas/ErrorEnvelope"
    NotFound:
      description: Database or table not found
      content:
        application/json:
          schema:
            $ref: "#/components/schemas/ErrorEnvelope"
    BadRequest:
      description: Invalid request
      content:
        application/json:
          schema:
            $ref: "#/components/schemas/ErrorEnvelope"
```

- [ ] **Step 4: Embed spec and add handler + route**

In `internal/api/router.go`, add to imports:

```go
_ "embed"
"encoding/json"
"gopkg.in/yaml.v3"
```

Add the embed directive and conversion before the `Server` struct:

```go
//go:embed ../../api/openapi.yaml
var openapiYAML []byte

// openapiJSON is the spec converted to JSON at init time.
var openapiJSON []byte

func init() {
	var raw interface{}
	if err := yaml.Unmarshal(openapiYAML, &raw); err != nil {
		panic("openapi: invalid YAML: " + err.Error())
	}
	var err error
	openapiJSON, err = json.Marshal(raw)
	if err != nil {
		panic("openapi: JSON marshal: " + err.Error())
	}
}
```

Add the handler method (after `handleHealth` in handlers.go or in router.go — keep it in router.go since it's tiny and tied to the embed):

```go
// handleOpenAPI serves the embedded OpenAPI spec as JSON.
func handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(openapiJSON)
}
```

In the `Handler` method, add the route to the **outer** mux (unauthenticated, alongside health):

```go
outer.HandleFunc("GET /v1/openapi.json", handleOpenAPI)
```

So the outer mux section becomes:

```go
	// Outer mux: public routes + protected API.
	outer := http.NewServeMux()
	outer.HandleFunc("GET /v1/health", s.handleHealth)
	outer.HandleFunc("GET /v1/openapi.json", handleOpenAPI)
	outer.Handle("/", protected)
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd C:/dev/mdbapi && go test ./internal/api/ -run TestOpenAPIEndpoint -v -short -count=1`
Expected: PASS

- [ ] **Step 6: Run full test suite**

Run: `cd C:/dev/mdbapi && go test ./... -short -count=1`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
git add api/openapi.yaml internal/api/router.go internal/api/api_test.go
git commit -m "feat: serve OpenAPI 3.1 spec at /v1/openapi.json"
```

---

### Task 8: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Add watcher and OpenAPI to CLAUDE.md**

In the **Architecture** section of `CLAUDE.md`, add two bullet points:

```markdown
- **OpenAPI spec**: Hand-written `api/openapi.yaml`, embedded in binary, served as JSON at `GET /v1/openapi.json` (unauthenticated).
- **Glob watcher**: `internal/watcher/` monitors filesystem for new databases matching configured globs. Dual mechanism: fsnotify for instant detection + periodic poll (30s) as fallback. New databases registered at runtime without restart.
```

In the **Key dependencies** table, add:

```markdown
| `fsnotify/fsnotify` | Filesystem event notifications for glob watcher |
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: add OpenAPI and glob watcher to CLAUDE.md"
```
