package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/marcfargas/mdbapi/internal/mdb"
)

const authFailDelay = 500 * time.Millisecond

// Server holds the dependencies for all HTTP handlers.
type Server struct {
	pool    *mdb.Pool
	maxRows int
	version string

	cacheMu sync.Mutex
	caches  map[string]*mdb.ColumnCache
}

// NewServer creates a new API server.
func NewServer(pool *mdb.Pool, maxRows int, version string) *Server {
	return &Server{
		pool:    pool,
		maxRows: maxRows,
		version: version,
		caches:  make(map[string]*mdb.ColumnCache),
	}
}

// Handler builds and returns the root http.Handler with all middleware applied.
func (s *Server) Handler(keys []string, maxBodyBytes int64) http.Handler {
	mux := http.NewServeMux()

	// Exact root match returns database list.
	mux.HandleFunc("GET /v1/{$}", s.handleListDatabases)
	mux.HandleFunc("GET /v1/version", s.handleVersion)
	mux.HandleFunc("GET /v1/{db}/tables", s.handleListTables)
	mux.HandleFunc("GET /v1/{db}/{table}", s.handleQueryTable)
	mux.HandleFunc("POST /v1/{db}/query", s.handleExecuteSQL)

	var h http.Handler = mux
	h = authMiddleware(keys, authFailDelay, h)
	h = maxBodyMiddleware(maxBodyBytes, h)
	h = loggingMiddleware(h)
	h = recoveryMiddleware(h)
	return h
}

// schemaCache returns the ColumnCache for a given alias, creating it on first use.
func (s *Server) schemaCache(alias string) *mdb.ColumnCache {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if c, ok := s.caches[alias]; ok {
		return c
	}
	db, _ := s.pool.Get(alias) // alias already validated by caller
	c := mdb.NewColumnCache(db)
	s.caches[alias] = c
	return c
}
