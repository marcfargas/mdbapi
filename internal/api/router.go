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
	// Inner mux: auth-protected API routes.
	inner := http.NewServeMux()
	inner.HandleFunc("GET /v1/{$}", s.handleListDatabases)
	inner.HandleFunc("GET /v1/version", s.handleVersion)
	inner.HandleFunc("GET /v1/{db}/tables", s.handleListTables)
	inner.HandleFunc("GET /v1/{db}/{table}", s.handleQueryTable)
	inner.HandleFunc("POST /v1/{db}/query", s.handleExecuteSQL)

	var protected http.Handler = inner
	protected = authMiddleware(keys, authFailDelay, protected)
	protected = maxBodyMiddleware(maxBodyBytes, protected)

	// Outer mux: public health route + protected API.
	outer := http.NewServeMux()
	outer.HandleFunc("GET /v1/health", s.handleHealth)
	outer.Handle("/", protected)

	var h http.Handler = outer
	h = loggingMiddleware(h)
	h = recoveryMiddleware(h)
	return h
}
