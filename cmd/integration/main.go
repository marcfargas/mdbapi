//go:build windows && amd64

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marcfargas/mdbapi/internal/api"
	"github.com/marcfargas/mdbapi/internal/mdb"
)

const (
	testAPIKey     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	httpTimeout    = 10 * time.Second
	startupTimeout = 20 * time.Second
	maxRows        = 1000
	maxBodyBytes   = 1 << 20
)

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *apiError       `json:"error"`
}

type dbInfo struct {
	Alias  string `json:"alias"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

type checkRunner struct {
	total  int
	failed int
}

func (r *checkRunner) pass(name, detail string) {
	r.total++
	if detail == "" {
		fmt.Printf("PASS  %s\n", name)
		return
	}
	fmt.Printf("PASS  %s -- %s\n", name, detail)
}

func (r *checkRunner) fail(name string, err error) {
	r.total++
	r.failed++
	fmt.Printf("FAIL  %s -- %v\n", name, err)
}

func main() {
	dir := flag.String("dir", "", "directory to scan for .mdb files")
	flag.Parse()

	if *dir == "" {
		fatalf("-dir is required")
	}

	files, err := findMDBFiles(*dir)
	if err != nil {
		fatalf("scan dir: %v", err)
	}
	if len(files) == 0 {
		fatalf("no .mdb files found in %s", *dir)
	}

	allCfgs := makeDBConfigs(files)
	fmt.Printf("Found %d MDB files\n", len(allCfgs))

	// Try to open each file individually; skip files that fail due to driver
	// incompatibility (e.g. Access 97 files on a system with only ACE 2016).
	// Only fail if zero files can be opened.
	var dbCfgs []mdb.DBConfig
	for _, cfg := range allCfgs {
		p, err := mdb.NewPool([]mdb.DBConfig{cfg})
		if err != nil {
			fmt.Printf("  SKIP %s (%s): %v\n", cfg.Alias, cfg.Path, err)
			continue
		}
		p.Close()
		fmt.Printf("  OK   %s => %s\n", cfg.Alias, cfg.Path)
		dbCfgs = append(dbCfgs, cfg)
	}

	if len(dbCfgs) == 0 {
		fatalf("no MDB files could be opened — is an Access ODBC driver installed?")
	}

	pool, err := mdb.NewPool(dbCfgs)
	if err != nil {
		fatalf("create ODBC pool: %v", err)
	}
	defer pool.Close()

	store := mdb.NewPoolStore(pool)
	server := api.NewServer(store, maxRows, "integration")
	handler := server.Handler([]string{testAPIKey}, maxBodyBytes)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatalf("listen: %v", err)
	}
	defer ln.Close()

	httpSrv := &http.Server{Handler: handler}
	serveErrCh := make(chan error, 1)
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
		}
		close(serveErrCh)
	}()

	client := &http.Client{Timeout: httpTimeout}
	baseURL := "http://" + ln.Addr().String()

	if err := waitForHealth(client, baseURL, startupTimeout); err != nil {
		fatalf("server health startup check failed: %v", err)
	}

	runner := &checkRunner{}

	runnerCheckHealth(runner, client, baseURL)
	dbList := runnerCheckDatabases(runner, client, baseURL, dbCfgs)

	for _, cfg := range dbCfgs {
		runDatabaseChecks(runner, client, pool, baseURL, cfg, dbList)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = httpSrv.Shutdown(shutdownCtx)
	cancel()
	if err, ok := <-serveErrCh; ok && err != nil {
		runner.fail("server shutdown", err)
	}

	fmt.Printf("\nSummary: %d checks, %d failed\n", runner.total, runner.failed)
	if runner.failed > 0 {
		os.Exit(1)
	}
}

func runnerCheckHealth(r *checkRunner, client *http.Client, baseURL string) {
	name := "GET /v1/health returns 200 and status=ok"
	env, status, raw, err := doRequest(client, "GET", baseURL+"/v1/health", false, nil)
	if err != nil {
		r.fail(name, err)
		return
	}
	if status != http.StatusOK {
		r.fail(name, fmt.Errorf("status=%d body=%s", status, raw))
		return
	}
	var data struct {
		Status string `json:"status"`
	}
	if err := decodeData(env, &data); err != nil {
		r.fail(name, err)
		return
	}
	if data.Status != "ok" {
		r.fail(name, fmt.Errorf("status=%q", data.Status))
		return
	}
	r.pass(name, "status ok")
}

func runnerCheckDatabases(r *checkRunner, client *http.Client, baseURL string, dbCfgs []mdb.DBConfig) map[string]dbInfo {
	name := "GET /v1/ returns configured DBs with status=ok"
	env, status, raw, err := doRequest(client, "GET", baseURL+"/v1/", true, nil)
	if err != nil {
		r.fail(name, err)
		return map[string]dbInfo{}
	}
	if status != http.StatusOK {
		r.fail(name, fmt.Errorf("status=%d body=%s", status, raw))
		return map[string]dbInfo{}
	}

	var data struct {
		Databases []dbInfo `json:"databases"`
	}
	if err := decodeData(env, &data); err != nil {
		r.fail(name, err)
		return map[string]dbInfo{}
	}

	byAlias := make(map[string]dbInfo, len(data.Databases))
	for _, db := range data.Databases {
		byAlias[db.Alias] = db
	}

	var (
		missing        []string
		hasStatusError bool
	)
	for _, cfg := range dbCfgs {
		db, ok := byAlias[cfg.Alias]
		if !ok {
			missing = append(missing, cfg.Alias)
			continue
		}
		if db.Status != "ok" {
			hasStatusError = true
			r.fail(name+" ("+cfg.Alias+")", fmt.Errorf("database status=%q", db.Status))
		}
	}
	if len(missing) > 0 {
		r.fail(name, fmt.Errorf("missing aliases: %s", strings.Join(missing, ", ")))
		return byAlias
	}
	if hasStatusError {
		return byAlias
	}

	r.pass(name, fmt.Sprintf("returned=%d", len(data.Databases)))
	return byAlias
}

func runDatabaseChecks(r *checkRunner, client *http.Client, pool *mdb.Pool, baseURL string, cfg mdb.DBConfig, dbList map[string]dbInfo) {
	dbName := cfg.Alias
	if db, ok := dbList[dbName]; ok && db.Status != "ok" {
		r.fail("GET /v1/ db status for "+dbName, fmt.Errorf("status=%q", db.Status))
		return
	}

	tables, ok := checkTablesEndpoint(r, client, baseURL, dbName)
	if !ok {
		return
	}

	table := tables[0]
	expectedCols, err := getSchemaColumns(pool, dbName, table)
	if err != nil {
		r.fail(fmt.Sprintf("%s schema for table %s", dbName, table), err)
		return
	}

	checkTableEndpoint(r, client, baseURL, dbName, table, expectedCols)
	checkSQLSelectEndpoint(r, client, baseURL, dbName, table, expectedCols)
	checkSQLDeleteForbidden(r, client, baseURL, dbName, table)
}

func checkTablesEndpoint(r *checkRunner, client *http.Client, baseURL, db string) ([]string, bool) {
	name := fmt.Sprintf("GET /v1/%s/tables returns non-empty list", db)
	url := fmt.Sprintf("%s/v1/%s/tables", baseURL, url.PathEscape(db))
	env, status, raw, err := doRequest(client, "GET", url, true, nil)
	if err != nil {
		r.fail(name, err)
		return nil, false
	}
	if status != http.StatusOK {
		r.fail(name, fmt.Errorf("status=%d body=%s", status, raw))
		return nil, false
	}
	var data struct {
		Tables []string `json:"tables"`
	}
	if err := decodeData(env, &data); err != nil {
		r.fail(name, err)
		return nil, false
	}
	if len(data.Tables) == 0 {
		r.fail(name, fmt.Errorf("tables list is empty"))
		return nil, false
	}
	r.pass(name, fmt.Sprintf("tables=%d", len(data.Tables)))
	return data.Tables, true
}

func checkTableEndpoint(r *checkRunner, client *http.Client, baseURL, db, table string, expectedCols []string) {
	name := fmt.Sprintf("GET /v1/%s/%s returns rows array and schema-compatible columns", db, table)
	url := fmt.Sprintf("%s/v1/%s/%s", baseURL, url.PathEscape(db), url.PathEscape(table))
	env, status, raw, err := doRequest(client, "GET", url, true, nil)
	if err != nil {
		r.fail(name, err)
		return
	}
	if status != http.StatusOK {
		r.fail(name, fmt.Errorf("status=%d body=%s", status, raw))
		return
	}

	var data struct {
		Rows []map[string]interface{} `json:"rows"`
	}
	if err := decodeData(env, &data); err != nil {
		r.fail(name, err)
		return
	}
	if data.Rows == nil {
		r.fail(name, fmt.Errorf("rows is missing or not an array"))
		return
	}

	if err := validateRowsAgainstSchema(data.Rows, expectedCols); err != nil {
		r.fail(name, err)
		return
	}

	detail := fmt.Sprintf("rows=%d", len(data.Rows))
	if len(data.Rows) == 0 {
		detail += " (table may be empty; schema checked via metadata)"
	}
	r.pass(name, detail)
}

func checkSQLSelectEndpoint(r *checkRunner, client *http.Client, baseURL, db, table string, expectedCols []string) {
	name := fmt.Sprintf("POST /v1/%s/query SELECT TOP 5 * FROM %s returns 200", db, table)
	url := fmt.Sprintf("%s/v1/%s/query", baseURL, url.PathEscape(db))
	query := fmt.Sprintf("SELECT TOP 5 * FROM %s", quoteIdent(table))
	env, status, raw, err := doRequest(client, "POST", url, true, map[string]interface{}{"sql": query})
	if err != nil {
		r.fail(name, err)
		return
	}
	if status != http.StatusOK {
		r.fail(name, fmt.Errorf("status=%d body=%s", status, raw))
		return
	}

	var data struct {
		Rows []map[string]interface{} `json:"rows"`
	}
	if err := decodeData(env, &data); err != nil {
		r.fail(name, err)
		return
	}
	if data.Rows == nil {
		r.fail(name, fmt.Errorf("rows is missing or not an array"))
		return
	}
	if err := validateRowsAgainstSchema(data.Rows, expectedCols); err != nil {
		r.fail(name, fmt.Errorf("row/schema mismatch: %w", err))
		return
	}
	r.pass(name, fmt.Sprintf("rows=%d", len(data.Rows)))
}

func checkSQLDeleteForbidden(r *checkRunner, client *http.Client, baseURL, db, table string) {
	name := fmt.Sprintf("POST /v1/%s/query DELETE FROM %s returns 403", db, table)
	url := fmt.Sprintf("%s/v1/%s/query", baseURL, url.PathEscape(db))
	query := fmt.Sprintf("DELETE FROM %s", quoteIdent(table))
	env, status, raw, err := doRequest(client, "POST", url, true, map[string]interface{}{"sql": query})
	if err != nil {
		r.fail(name, err)
		return
	}
	if status != http.StatusForbidden {
		r.fail(name, fmt.Errorf("status=%d body=%s", status, raw))
		return
	}
	if env.Error == nil || env.Error.Code != "forbidden" {
		r.fail(name, fmt.Errorf("expected error.code=forbidden, got %+v", env.Error))
		return
	}
	r.pass(name, env.Error.Message)
}

func validateRowsAgainstSchema(rows []map[string]interface{}, expected []string) error {
	if len(expected) == 0 {
		return fmt.Errorf("expected schema has zero columns")
	}
	if len(rows) == 0 {
		return nil
	}

	expectedSet := make(map[string]struct{}, len(expected))
	for _, col := range expected {
		expectedSet[col] = struct{}{}
	}

	for i, row := range rows {
		if len(row) != len(expectedSet) {
			return fmt.Errorf("row %d key count=%d, expected=%d", i, len(row), len(expectedSet))
		}
		for key := range row {
			if _, ok := expectedSet[key]; !ok {
				return fmt.Errorf("row %d has unexpected column %q", i, key)
			}
		}
	}
	return nil
}

func getSchemaColumns(pool *mdb.Pool, alias, table string) ([]string, error) {
	db, err := pool.Get(alias)
	if err != nil {
		return nil, err
	}
	cache := mdb.NewColumnCache(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	meta, err := cache.Columns(ctx, table)
	if err != nil {
		return nil, err
	}
	cols := make([]string, len(meta))
	for i, c := range meta {
		cols[i] = c.Name
	}
	return cols, nil
}

func waitForHealth(client *http.Client, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		env, status, _, err := doRequest(client, "GET", baseURL+"/v1/health", false, nil)
		if err == nil && status == http.StatusOK && env.Error == nil {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("status=%d", status)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for health: %v", lastErr)
}

func doRequest(client *http.Client, method, reqURL string, auth bool, body interface{}) (*envelope, int, string, error) {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, "", fmt.Errorf("marshal request body: %w", err)
		}
		payload = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, reqURL, payload)
	if err != nil {
		return nil, 0, "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+testAPIKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, "", err
	}

	env := &envelope{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, env); err != nil {
			return nil, resp.StatusCode, string(raw), fmt.Errorf("decode envelope: %w", err)
		}
	}
	return env, resp.StatusCode, string(raw), nil
}

func decodeData(env *envelope, out interface{}) error {
	if env == nil {
		return fmt.Errorf("nil response envelope")
	}
	if env.Error != nil {
		return fmt.Errorf("api error: %s (%s)", env.Error.Message, env.Error.Code)
	}
	if len(env.Data) == 0 {
		return fmt.Errorf("response missing data")
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decode data: %w", err)
	}
	return nil
}

func findMDBFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".mdb") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func makeDBConfigs(paths []string) []mdb.DBConfig {
	used := make(map[string]int)
	cfgs := make([]mdb.DBConfig, 0, len(paths))
	for _, p := range paths {
		alias := sanitizeAlias(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
		if alias == "" {
			alias = "db"
		}
		if n, ok := used[alias]; ok {
			n++
			used[alias] = n
			alias = fmt.Sprintf("%s_%d", alias, n)
		} else {
			used[alias] = 1
		}
		cfgs = append(cfgs, mdb.DBConfig{Alias: alias, Path: p})
	}
	return cfgs
}

func sanitizeAlias(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func quoteIdent(s string) string {
	return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
