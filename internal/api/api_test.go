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
// Rate limiting is disabled in tests to avoid flaky timing-dependent failures.
func newTestServer(store mdb.Store) http.Handler {
	s := NewServer(store, 1000, "test")
	key := strings.Repeat("k", 32)
	return s.Handler(HandlerConfig{
		Keys:         []string{key},
		MaxBodyBytes: 1 << 20,
		RateLimit:    0, // disabled in tests
	})
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
		// Health endpoint must NOT expose version or database details.
		if data["version"] != nil {
			t.Errorf("health should not expose version, got %v", data["version"])
		}
		if data["databases"] != nil {
			t.Errorf("health should not expose database details, got %v", data["databases"])
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
	})
}

func TestSecurityHeaders(t *testing.T) {
	store := &mockStore{aliases: []string{}}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	checks := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":       "DENY",
		"Cache-Control":         "no-store",
		"Content-Security-Policy": "default-src 'none'",
	}
	for header, want := range checks {
		if got := w.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestIPAllowList(t *testing.T) {
	store := &mockStore{aliases: []string{}}
	s := NewServer(store, 1000, "test")
	key := strings.Repeat("k", 32)

	t.Run("allowed IP passes", func(t *testing.T) {
		h := s.Handler(HandlerConfig{
			Keys:         []string{key},
			MaxBodyBytes: 1 << 20,
			AllowedIPs:   []string{"192.0.2.1"},
		})

		req := httptest.NewRequest("GET", "/v1/health", nil)
		req.RemoteAddr = "192.0.2.1:12345"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 for allowed IP, got %d", w.Code)
		}
	})

	t.Run("blocked IP rejected", func(t *testing.T) {
		h := s.Handler(HandlerConfig{
			Keys:         []string{key},
			MaxBodyBytes: 1 << 20,
			AllowedIPs:   []string{"192.0.2.1"},
		})

		req := httptest.NewRequest("GET", "/v1/health", nil)
		req.RemoteAddr = "10.0.0.99:12345"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403 for blocked IP, got %d", w.Code)
		}
	})

	t.Run("CIDR match", func(t *testing.T) {
		h := s.Handler(HandlerConfig{
			Keys:         []string{key},
			MaxBodyBytes: 1 << 20,
			AllowedIPs:   []string{"10.0.0.0/8"},
		})

		req := httptest.NewRequest("GET", "/v1/health", nil)
		req.RemoteAddr = "10.99.1.42:12345"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 for CIDR-matched IP, got %d", w.Code)
		}
	})

	t.Run("empty allow list allows all", func(t *testing.T) {
		h := s.Handler(HandlerConfig{
			Keys:         []string{key},
			MaxBodyBytes: 1 << 20,
			AllowedIPs:   nil,
		})

		req := httptest.NewRequest("GET", "/v1/health", nil)
		req.RemoteAddr = "203.0.113.50:9999"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 when allow list is empty, got %d", w.Code)
		}
	})
}

func TestRateLimiting(t *testing.T) {
	store := &mockStore{aliases: []string{}}
	s := NewServer(store, 1000, "test")
	key := strings.Repeat("k", 32)

	// Very low limit: 1 req/s, burst 2.
	h := s.Handler(HandlerConfig{
		Keys:         []string{key},
		MaxBodyBytes: 1 << 20,
		RateLimit:    1,
		RateBurst:    2,
	})

	// First 2 requests (burst) should succeed.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/v1/health", nil)
		req.RemoteAddr = "192.0.2.1:12345"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, w.Code)
		}
	}

	// Third request should be rate-limited.
	req := httptest.NewRequest("GET", "/v1/health", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 after burst exhausted, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("expected Retry-After header on 429 response")
	}

	// Different IP should still work (per-IP limiting).
	req2 := httptest.NewRequest("GET", "/v1/health", nil)
	req2.RemoteAddr = "192.0.2.99:12345"
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("different IP should not be rate-limited, got %d", w2.Code)
	}
}

func TestInternalErrorDoesNotLeakDetails(t *testing.T) {
	store := &mockStore{
		aliases: []string{"inv"},
		tables:  map[string][]string{"inv": {"Products"}},
		err:     fmt.Errorf("ODBC: driver not found at C:\\Windows\\system32\\aceodbc.dll"),
	}
	h := newTestServer(store)

	req := httptest.NewRequest("GET", "/v1/inv/tables", nil)
	req.Header.Set("Authorization", authHeader())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	// Response must not contain the ODBC error details.
	body := w.Body.String()
	if strings.Contains(body, "ODBC") || strings.Contains(body, "aceodbc") || strings.Contains(body, "C:\\") {
		t.Errorf("internal error response leaks details: %s", body)
	}
	// Should contain the generic message.
	if !strings.Contains(body, "internal server error") {
		t.Errorf("expected generic 'internal server error', got: %s", body)
	}
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
