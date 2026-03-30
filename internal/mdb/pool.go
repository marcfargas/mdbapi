package mdb

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
	// "odbc" driver is registered via driver_windows_amd64.go
)

const (
	// MaxOpenConns is the per-database connection limit. Access file-level locking
	// degrades quickly above ~5 concurrent readers; keep conservative.
	MaxOpenConns    = 3
	MaxIdleConns    = 1
	ConnMaxLifetime = 10 * time.Minute
	QueryTimeout    = 30 * time.Second
)

// DBConfig is the configuration for a single database alias.
type DBConfig struct {
	Alias  string
	Path   string
	Driver string // optional: explicit ODBC driver name; empty = auto-detect
}

// DBInfo is a summary of a registered database returned by the listing endpoint.
type DBInfo struct {
	Alias      string     `json:"alias"`
	Path       string     `json:"path"`
	Status     string     `json:"status"`                // "ok" or "error: <message>"
	ModifiedAt *time.Time `json:"modified_at,omitempty"` // file mtime; nil if stat fails
}

// Pool manages a map of *sql.DB keyed by alias.
type Pool struct {
	mu       sync.RWMutex
	dbs      map[string]*sql.DB
	paths    map[string]string // alias → file path (for listings)
	connStrs map[string]string // alias → ODBC connection string (for catalog fallback)
}

// NewPool opens ODBC connections for all configured databases.
// All connections use ReadOnly=1 to prevent exclusive locks.
// Databases that fail to open are logged and skipped rather than
// crashing the service — they appear with status "error" in listings.
func NewPool(dbs []DBConfig) (*Pool, error) {
	p := &Pool{
		dbs:      make(map[string]*sql.DB, len(dbs)),
		paths:    make(map[string]string, len(dbs)),
		connStrs: make(map[string]string, len(dbs)),
	}
	for _, cfg := range dbs {
		db, connStr, err := openDB(cfg.Path, cfg.Driver)
		if err != nil {
			slog.Warn("skipping database that failed to open",
				"alias", cfg.Alias, "path", cfg.Path, "err", err)
			p.paths[cfg.Alias] = cfg.Path // still list it with error status
			continue
		}
		p.dbs[cfg.Alias] = db
		p.paths[cfg.Alias] = cfg.Path
		p.connStrs[cfg.Alias] = connStr
	}
	return p, nil
}

// ConnStrFor returns the ODBC connection string used to open alias.
// Used by PoolStore for catalog-level introspection (SQLTables fallback).
func (p *Pool) ConnStrFor(alias string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.connStrs[alias]
}

// Get returns the *sql.DB for a given alias, or ErrUnknownAlias.
func (p *Pool) Get(alias string) (*sql.DB, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	db, ok := p.dbs[alias]
	if !ok {
		return nil, ErrUnknownAlias{Alias: alias}
	}
	return db, nil
}

// Aliases returns all registered database aliases.
func (p *Pool) Aliases() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.paths))
	for alias := range p.paths {
		out = append(out, alias)
	}
	return out
}

// Databases returns a DBInfo for every registered database, including a live
// ping to report connection status. Callers should not hold the result for long;
// status reflects the instant the method was called.
func (p *Pool) Databases() []DBInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]DBInfo, 0, len(p.paths))
	for alias, path := range p.paths {
		info := DBInfo{
			Alias: alias,
			Path:  path,
		}
		db, ok := p.dbs[alias]
		if !ok {
			info.Status = "error: failed to open"
		} else if err := db.Ping(); err != nil {
			info.Status = "error: " + err.Error()
		} else {
			info.Status = "ok"
		}
		if fi, err := os.Stat(path); err == nil {
			t := fi.ModTime()
			info.ModifiedAt = &t
		}
		out = append(out, info)
	}
	return out
}

// Ping tests all connections and returns a map of alias → error.
func (p *Pool) Ping() map[string]error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	results := make(map[string]error, len(p.dbs))
	for alias, db := range p.dbs {
		results[alias] = db.Ping()
	}
	return results
}

// Close closes all database connections.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for alias, db := range p.dbs {
		_ = db.Close()
		delete(p.dbs, alias)
	}
}

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

// ErrUnknownAlias is returned when a database alias is not in the pool.
type ErrUnknownAlias struct {
	Alias string
}

func (e ErrUnknownAlias) Error() string {
	return fmt.Sprintf("unknown database alias: %q", e.Alias)
}
