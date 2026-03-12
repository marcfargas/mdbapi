package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"

	"github.com/marcfargas/mdbapi/internal/mdb"
)

// handleListDatabases returns all exposed databases with path and live status.
// GET /v1/
func (s *Server) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	dbs := s.store.Databases()
	sort.Slice(dbs, func(i, j int) bool { return dbs[i].Alias < dbs[j].Alias })
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"databases": dbs,
	})
}

// handleListTables returns all user tables in the given database.
// GET /v1/{db}/tables
func (s *Server) handleListTables(w http.ResponseWriter, r *http.Request) {
	dbAlias := r.PathValue("db")
	tables, err := s.store.ListTables(r.Context(), dbAlias)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"database": dbAlias,
		"tables":   tables,
	})
}

// handleQueryTable returns rows from the given table with optional filtering.
// GET /v1/{db}/{table}?field=value&limit=N&offset=N&sort=col&sort_desc=true
func (s *Server) handleQueryTable(w http.ResponseWriter, r *http.Request) {
	dbAlias := r.PathValue("db")
	tableName := r.PathValue("table")

	opts := mdb.ParseQueryOpts(r.URL.Query())
	result, err := s.store.QueryTable(r.Context(), dbAlias, tableName, opts, s.maxRows)
	if err != nil {
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"database": dbAlias,
		"table":    tableName,
		"count":    len(result.Rows),
		"rows":     result.Rows,
	})
}

// handleExecuteSQL runs a read-only SQL passthrough query.
// POST /v1/{db}/query
// Body: { "sql": "SELECT ...", "params": [...] }
func (s *Server) handleExecuteSQL(w http.ResponseWriter, r *http.Request) {
	dbAlias := r.PathValue("db")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		badRequest(w, "failed to read request body")
		return
	}

	var req struct {
		SQL    string        `json:"sql"`
		Params []interface{} `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		badRequest(w, "invalid JSON body")
		return
	}
	if req.SQL == "" {
		badRequest(w, "sql field is required")
		return
	}

	result, err := s.store.ExecuteSQL(r.Context(), dbAlias, req.SQL, req.Params, s.maxRows)
	if err != nil {
		if errors.Is(err, mdb.ErrWriteNotAllowed) {
			forbidden(w, "only SELECT statements are allowed")
			return
		}
		if isNotFound(err) {
			notFound(w)
			return
		}
		internalError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"database": dbAlias,
		"count":    len(result.Rows),
		"rows":     result.Rows,
	})
}

// handleVersion returns the binary version.
// GET /v1/version
func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version": s.version,
	})
}

// handleHealth returns a minimal status for liveness/readiness probes.
// This endpoint is unauthenticated, so it intentionally omits version
// and database details to avoid information leakage.
// GET /v1/health
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	dbs := s.store.Databases()
	healthy := true
	for _, db := range dbs {
		if db.Status != "ok" {
			healthy = false
			break
		}
	}
	status := "ok"
	if !healthy {
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": status,
	})
}

// isNotFound returns true for errors that should map to HTTP 404.
func isNotFound(err error) bool {
	var unknownAlias mdb.ErrUnknownAlias
	return errors.As(err, &unknownAlias)
}
