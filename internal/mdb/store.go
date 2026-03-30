package mdb

import (
	"context"
	"sync"
)

// Store is the interface consumed by API handlers.
// It abstracts Pool+functions so handlers can be tested without ODBC.
type Store interface {
	// Aliases returns all registered database aliases (fast, no I/O).
	Aliases() []string
	// Databases returns alias, path, and live connection status for every
	// registered database. Performs a ping per database; use sparingly.
	Databases() []DBInfo
	// ListTables returns user table names for the given alias.
	ListTables(ctx context.Context, alias string) ([]string, error)
	// QueryTable runs a filtered, paginated SELECT against tableName.
	QueryTable(ctx context.Context, alias, tableName string, opts QueryOpts, maxRows int) (*QueryResult, error)
	// ExecuteSQL runs a read-only SQL passthrough query.
	ExecuteSQL(ctx context.Context, alias, query string, params []interface{}, maxRows int, trim bool) (*QueryResult, error)
}

// PoolStore implements Store on top of a Pool.
type PoolStore struct {
	pool    *Pool
	cacheMu sync.Mutex
	caches  map[string]*ColumnCache
}

// NewPoolStore wraps a Pool as a Store.
func NewPoolStore(pool *Pool) *PoolStore {
	return &PoolStore{
		pool:   pool,
		caches: make(map[string]*ColumnCache),
	}
}

func (s *PoolStore) Aliases() []string {
	return s.pool.Aliases()
}

func (s *PoolStore) Databases() []DBInfo {
	return s.pool.Databases()
}

func (s *PoolStore) ListTables(ctx context.Context, alias string) ([]string, error) {
	connStr := s.pool.ConnStrFor(alias)
	if connStr == "" {
		return nil, ErrUnknownAlias{Alias: alias}
	}
	return listTablesViaODBCSyscall(ctx, connStr)
}

func (s *PoolStore) QueryTable(ctx context.Context, alias, tableName string, opts QueryOpts, maxRows int) (*QueryResult, error) {
	db, err := s.pool.Get(alias)
	if err != nil {
		return nil, err
	}
	return QueryTable(ctx, db, s.cache(alias), tableName, opts, maxRows)
}

func (s *PoolStore) ExecuteSQL(ctx context.Context, alias, query string, params []interface{}, maxRows int, trim bool) (*QueryResult, error) {
	db, err := s.pool.Get(alias)
	if err != nil {
		return nil, err
	}
	return ExecuteSQL(ctx, db, query, params, maxRows, trim)
}

// cache returns the ColumnCache for an alias, creating it lazily.
func (s *PoolStore) cache(alias string) *ColumnCache {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if c, ok := s.caches[alias]; ok {
		return c
	}
	db, _ := s.pool.Get(alias)
	c := NewColumnCache(db)
	s.caches[alias] = c
	return c
}
