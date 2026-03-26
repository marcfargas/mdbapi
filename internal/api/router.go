package api

import (
	"encoding/json"
	"net/http"
	"time"

	openapidefs "github.com/marcfargas/mdbapi/api"
	"github.com/marcfargas/mdbapi/internal/mdb"
	"gopkg.in/yaml.v3"
)

// openapiJSON is the spec converted to JSON at init time.
var openapiJSON []byte

func init() {
	var raw interface{}
	if err := yaml.Unmarshal(openapidefs.OpenAPIYAML, &raw); err != nil {
		panic("openapi: invalid YAML: " + err.Error())
	}
	var err error
	openapiJSON, err = json.Marshal(raw)
	if err != nil {
		panic("openapi: JSON marshal: " + err.Error())
	}
}

// handleOpenAPI serves the embedded OpenAPI spec as JSON.
func handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(openapiJSON)
}

const authFailDelay = 500 * time.Millisecond

// Server holds the dependencies for all HTTP handlers.
type Server struct {
	store   mdb.Store
	maxRows int
	version string
	commit  string
}

// NewServer creates a new API server.
func NewServer(store mdb.Store, maxRows int, version, commit string) *Server {
	return &Server{
		store:   store,
		maxRows: maxRows,
		version: version,
		commit:  commit,
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
	outer.HandleFunc("GET /v1/openapi.json", handleOpenAPI)
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
