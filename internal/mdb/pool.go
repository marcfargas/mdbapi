package mdb

import (
	"database/sql"
	"fmt"
	"sync"
	"time"
	// "odbc" driver is registered via driver_windows_amd64.go
)

const (
	// MaxOpenConns is the per-database connection limit. Access file-level locking
	// degrades quickly above ~5 concurrent readers; keep conservative.
	MaxOpenConns = 3
	MaxIdleConns = 1
	ConnMaxLifetime = 10 * time.Minute
	QueryTimeout    = 30 * time.Second
)

// DBConfig is the configuration for a single database alias.
type DBConfig struct {
	Alias string
	Path  string
}

// DBInfo is a summary of a registered database returned by the listing endpoint.
type DBInfo struct {
	Alias  string `json:"alias"`
	Path   string `json:"path"`
	Status string `json:"status"` // "ok" or "error: <message>"
}

// Pool manages a map of *sql.DB keyed by alias.
type Pool struct {
	mu    sync.RWMutex
	dbs   map[string]*sql.DB
	paths map[string]string // alias → file path (for listings)
}

// NewPool opens ODBC connections for all configured databases.
// All connections use ReadOnly=1 to prevent exclusive locks.
func NewPool(dbs []DBConfig) (*Pool, error) {
	p := &Pool{
		dbs:   make(map[string]*sql.DB, len(dbs)),
		paths: make(map[string]string, len(dbs)),
	}
	for _, cfg := range dbs {
		db, err := openDB(cfg.Path)
		if err != nil {
			// Close any already-opened connections before returning.
			p.Close()
			return nil, fmt.Errorf("open database %q (%s): %w", cfg.Alias, cfg.Path, err)
		}
		p.dbs[cfg.Alias] = db
		p.paths[cfg.Alias] = cfg.Path
	}
	return p, nil
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
	out := make([]string, 0, len(p.dbs))
	for alias := range p.dbs {
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

// openDB creates an ODBC connection to an Access database (read-only).
// The ReadOnly=1 parameter prevents exclusive locks and write operations
// at the driver level — a belt-and-suspenders complement to SELECT-only SQL.
func openDB(path string) (*sql.DB, error) {
	connStr := fmt.Sprintf(
		`DRIVER={Microsoft Access Driver (*.mdb, *.accdb)};DBQ=%s;ReadOnly=1;`,
		path,
	)
	db, err := sql.Open("odbc", connStr)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(MaxOpenConns)
	db.SetMaxIdleConns(MaxIdleConns)
	db.SetConnMaxLifetime(ConnMaxLifetime)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// ErrUnknownAlias is returned when a database alias is not in the pool.
type ErrUnknownAlias struct {
	Alias string
}

func (e ErrUnknownAlias) Error() string {
	return fmt.Sprintf("unknown database alias: %q", e.Alias)
}
