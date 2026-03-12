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

// HandlerConfig holds all parameters needed to build the HTTP handler chain.
type HandlerConfig struct {
	Keys           []string
	MaxBodyBytes   int64
	AllowedIPs     []string
	RateLimit      float64
	RateBurst      int
	RequestTimeout time.Duration
}

// Handler builds and returns the root http.Handler with all middleware applied.
//
// Middleware order (outermost first):
//  1. Recovery (panic → 500)
//  2. Security headers
//  3. Logging
//  4. IP allow list
//  5. Rate limiting (per-IP)
//  6. Request timeout
//  7. Body size limit
//  8. Auth (on protected routes only)
func (s *Server) Handler(cfg HandlerConfig) http.Handler {
	// Inner mux: auth-protected API routes.
	inner := http.NewServeMux()
	inner.HandleFunc("GET /v1/{$}", s.handleListDatabases)
	inner.HandleFunc("GET /v1/version", s.handleVersion)
	inner.HandleFunc("GET /v1/{db}/tables", s.handleListTables)
	inner.HandleFunc("GET /v1/{db}/{table}", s.handleQueryTable)
	inner.HandleFunc("POST /v1/{db}/query", s.handleExecuteSQL)

	var protected http.Handler = inner
	protected = authMiddleware(cfg.Keys, authFailDelay, protected)

	// Outer mux: public health route + protected API.
	outer := http.NewServeMux()
	outer.HandleFunc("GET /v1/health", s.handleHealth)
	outer.Handle("/", protected)

	// Build middleware chain (applied bottom-up, so first listed = outermost).
	var h http.Handler = outer
	h = maxBodyMiddleware(cfg.MaxBodyBytes, h)
	h = requestTimeoutMiddleware(cfg.RequestTimeout, h)
	h = rateLimitMiddleware(cfg.RateLimit, cfg.RateBurst, h)
	h = ipAllowMiddleware(cfg.AllowedIPs, h)
	h = loggingMiddleware(h)
	h = securityHeadersMiddleware(h)
	h = recoveryMiddleware(h)
	return h
}
