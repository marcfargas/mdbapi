package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/marcfargas/mdbapi/internal/mdb"
)

// mockStore implements mdb.Store for handler tests without ODBC.
type mockStore struct {
	aliases   []string
	databases []mdb.DBInfo // if nil, derived from aliases with status "ok"
	tables    map[string][]string
	rows      *mdb.QueryResult
	err       error
}

func (m *mockStore) Aliases() []string { return m.aliases }

func (m *mockStore) Databases() []mdb.DBInfo {
	if m.databases != nil {
		return m.databases
	}
	out := make([]mdb.DBInfo, len(m.aliases))
	for i, a := range m.aliases {
		out[i] = mdb.DBInfo{Alias: a, Path: `C:\data\` + a + `.mdb`, Status: "ok"}
	}
	return out
}

func (m *mockStore) ListTables(_ context.Context, alias string) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	t, ok := m.tables[alias]
	if !ok {
		return nil, mdb.ErrUnknownAlias{Alias: alias}
	}
	return t, nil
}

func (m *mockStore) QueryTable(_ context.Context, alias, _ string, _ mdb.QueryOpts, _ int) (*mdb.QueryResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	if _, ok := m.tables[alias]; !ok {
		return nil, mdb.ErrUnknownAlias{Alias: alias}
	}
	if m.rows != nil {
		return m.rows, nil
	}
	return &mdb.QueryResult{Columns: []string{}, Rows: []map[string]interface{}{}}, nil
}

func (m *mockStore) ExecuteSQL(_ context.Context, alias, query string, _ []interface{}, _ int) (*mdb.QueryResult, error) {
	if m.err != nil {
		return nil, m.err
	}
	if _, ok := m.tables[alias]; !ok {
		return nil, mdb.ErrUnknownAlias{Alias: alias}
	}
	if err := validateReadOnlyForTest(query); err != nil {
		return nil, mdb.ErrWriteNotAllowed
	}
	if m.rows != nil {
		return m.rows, nil
	}
	return &mdb.QueryResult{Columns: []string{}, Rows: []map[string]interface{}{}}, nil
}

func validateReadOnlyForTest(query string) error {
	upper := strings.TrimSpace(strings.ToUpper(query))
	if strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH") {
		return nil
	}
	return fmt.Errorf("write not allowed")
}

// newTestServer creates a Server+Handler with a single test key.
func newTestServer(store mdb.Store) http.Handler {
	s := NewServer(store, 1000, "test")
	key := strings.Repeat("k", 32)
	return s.Handler([]string{key}, 1<<20)
}

func authHeader() string { return "Bearer " + strings.Repeat("k", 32) }

// --- tests ---

func TestListDatabases(t *testing.T) {
	store := &mockStore{aliases: []string{"inventory", "customers"}}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/", nil)
	req.Header.Set("Authorization", authHeader())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	data := body["data"].(map[string]interface{})
	dbs := data["databases"].([]interface{})
	if len(dbs) != 2 {
		t.Errorf("expected 2 databases, got %d", len(dbs))
	}
	// Must be sorted by alias.
	first := dbs[0].(map[string]interface{})
	if first["alias"] != "customers" {
		t.Errorf("databases not sorted: first alias = %q", first["alias"])
	}
	// Each entry must have alias, path, status.
	for _, raw := range dbs {
		entry := raw.(map[string]interface{})
		for _, field := range []string{"alias", "path", "status"} {
			if entry[field] == nil {
				t.Errorf("database entry missing field %q: %v", field, entry)
			}
		}
		if entry["status"] != "ok" {
			t.Errorf("expected status ok, got %q", entry["status"])
		}
	}
}

func TestListDatabases_Unauthorized(t *testing.T) {
	store := &mockStore{aliases: []string{"db1"}}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/", nil)
	// no auth header
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without auth, got %d", w.Code)
	}
}

func TestListDatabases_WrongKey(t *testing.T) {
	store := &mockStore{aliases: []string{"db1"}}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 with wrong key, got %d", w.Code)
	}
}

func TestListTables(t *testing.T) {
	store := &mockStore{
		aliases: []string{"inv"},
		tables:  map[string][]string{"inv": {"Products", "Orders"}},
	}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/inv/tables", nil)
	req.Header.Set("Authorization", authHeader())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestListTables_UnknownDB(t *testing.T) {
	store := &mockStore{
		aliases: []string{"inv"},
		tables:  map[string][]string{"inv": {"Products"}},
	}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/nosuchdb/tables", nil)
	req.Header.Set("Authorization", authHeader())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown DB, got %d", w.Code)
	}
}

func TestQueryTable(t *testing.T) {
	store := &mockStore{
		aliases: []string{"inv"},
		tables:  map[string][]string{"inv": {"Products"}},
		rows: &mdb.QueryResult{
			Columns: []string{"id", "name"},
			Rows: []map[string]interface{}{
				{"id": int64(1), "name": "Widget"},
			},
		},
	}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/inv/Products", nil)
	req.Header.Set("Authorization", authHeader())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	data := body["data"].(map[string]interface{})
	if data["count"].(float64) != 1 {
		t.Errorf("count = %v, want 1", data["count"])
	}
}

func TestExecuteSQL_Select(t *testing.T) {
	store := &mockStore{
		aliases: []string{"inv"},
		tables:  map[string][]string{"inv": {"Products"}},
	}
	h := newTestServer(store)

	body, _ := json.Marshal(map[string]interface{}{
		"sql": "SELECT * FROM Products",
	})
	req := httptest.NewRequest("POST", "/v1/inv/query", bytes.NewReader(body))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

func TestExecuteSQL_WriteRejected(t *testing.T) {
	store := &mockStore{
		aliases: []string{"inv"},
		tables:  map[string][]string{"inv": {"Products"}},
	}
	h := newTestServer(store)

	body, _ := json.Marshal(map[string]interface{}{
		"sql": "DELETE FROM Products",
	})
	req := httptest.NewRequest("POST", "/v1/inv/query", bytes.NewReader(body))
	req.Header.Set("Authorization", authHeader())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for DELETE, got %d", w.Code)
	}
}

func TestExecuteSQL_EmptySQL(t *testing.T) {
	store := &mockStore{
		aliases: []string{"inv"},
		tables:  map[string][]string{"inv": {"Products"}},
	}
	h := newTestServer(store)

	body, _ := json.Marshal(map[string]interface{}{"sql": ""})
	req := httptest.NewRequest("POST", "/v1/inv/query", bytes.NewReader(body))
	req.Header.Set("Authorization", authHeader())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty sql, got %d", w.Code)
	}
}

func TestVersion(t *testing.T) {
	store := &mockStore{}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/version", nil)
	req.Header.Set("Authorization", authHeader())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	data := body["data"].(map[string]interface{})
	if data["version"] != "test" {
		t.Errorf("version = %v, want test", data["version"])
	}
}

func TestHealth_NoAuth(t *testing.T) {
	t.Run("all databases ok", func(t *testing.T) {
		store := &mockStore{databases: []mdb.DBInfo{
			{Alias: "db1", Path: `C:\data\db1.mdb`, Status: "ok"},
			{Alias: "db2", Path: `C:\data\db2.mdb`, Status: "ok"},
		}}
		h := newTestServer(store)

		req := httptest.NewRequest("GET", "/v1/health", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
		}

		var body map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid json response: %v", err)
		}
		data := body["data"].(map[string]interface{})
		if data["status"] != "ok" {
			t.Errorf("status = %v, want ok", data["status"])
		}
		if data["version"] != "test" {
			t.Errorf("version = %v, want test", data["version"])
		}
		dbSummary := data["databases"].(map[string]interface{})
		if dbSummary["total"].(float64) != 2 {
			t.Errorf("databases.total = %v, want 2", dbSummary["total"])
		}
		if dbSummary["ok"].(float64) != 2 {
			t.Errorf("databases.ok = %v, want 2", dbSummary["ok"])
		}
		if dbSummary["error"].(float64) != 0 {
			t.Errorf("databases.error = %v, want 0", dbSummary["error"])
		}
	})

	t.Run("degraded when any database has non-ok status", func(t *testing.T) {
		store := &mockStore{databases: []mdb.DBInfo{
			{Alias: "db1", Path: `C:\data\db1.mdb`, Status: "ok"},
			{Alias: "db2", Path: `C:\data\db2.mdb`, Status: "error: timeout"},
			{Alias: "db3", Path: `C:\data\db3.mdb`, Status: "ok"},
		}}
		h := newTestServer(store)

		req := httptest.NewRequest("GET", "/v1/health", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
		}

		var body map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("invalid json response: %v", err)
		}
		data := body["data"].(map[string]interface{})
		if data["status"] != "degraded" {
			t.Errorf("status = %v, want degraded", data["status"])
		}
		dbSummary := data["databases"].(map[string]interface{})
		if dbSummary["total"].(float64) != 3 {
			t.Errorf("databases.total = %v, want 3", dbSummary["total"])
		}
		if dbSummary["ok"].(float64) != 2 {
			t.Errorf("databases.ok = %v, want 2", dbSummary["ok"])
		}
		if dbSummary["error"].(float64) != 1 {
			t.Errorf("databases.error = %v, want 1", dbSummary["error"])
		}
	})
}

func TestXAPIKeyHeader(t *testing.T) {
	store := &mockStore{aliases: []string{}}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/", nil)
	req.Header.Set("X-API-Key", strings.Repeat("k", 32)) // same key, different header
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("X-API-Key should work, got %d", w.Code)
	}
}
