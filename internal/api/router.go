package api

import (
	"net/http"
	"time"

	"github.com/marcfargas/mdbapi/internal/mdb"
)

const authFailDelay = 500 * time.Millisecond

// Server holds the dependencies for all HTTP handlers.
type Server struct {
	store   mdb.Store
	maxRows int
	version string
}

// NewServer creates a new API server.
func NewServer(store mdb.Store, maxRows int, version string) *Server {
	return &Server{
		store:   store,
		maxRows: maxRows,
		version: version,
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
