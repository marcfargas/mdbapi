package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"

	"github.com/marcfargas/mdbapi/internal/mdb"
)

// handleListDatabases returns all registered database aliases.
// GET /v1/
func (s *Server) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	aliases := s.pool.Aliases()
	sort.Strings(aliases)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"databases": aliases,
	})
}

// handleListTables returns all user tables in the given database.
// GET /v1/{db}/tables
func (s *Server) handleListTables(w http.ResponseWriter, r *http.Request) {
	dbAlias := r.PathValue("db")
	db, err := s.pool.Get(dbAlias)
	if err != nil {
		notFound(w)
		return
	}

	tables, err := mdb.ListTables(r.Context(), db)
	if err != nil {
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

	db, err := s.pool.Get(dbAlias)
	if err != nil {
		notFound(w)
		return
	}

	cache := s.schemaCache(dbAlias)
	opts := mdb.ParseQueryOpts(r.URL.Query())

	result, err := mdb.QueryTable(r.Context(), db, cache, tableName, opts, s.maxRows)
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

	db, err := s.pool.Get(dbAlias)
	if err != nil {
		notFound(w)
		return
	}

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

	result, err := mdb.ExecuteSQL(r.Context(), db, req.SQL, req.Params, s.maxRows)
	if err != nil {
		if errors.Is(err, mdb.ErrWriteNotAllowed) {
			forbidden(w, "only SELECT statements are allowed")
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

// isNotFound returns true for errors that should map to HTTP 404.
func isNotFound(err error) bool {
	var unknownAlias mdb.ErrUnknownAlias
	return errors.As(err, &unknownAlias)
}
