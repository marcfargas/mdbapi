# OpenAPI Spec + Glob Watcher — Design Spec

**Date**: 2026-03-26
**Status**: Draft
**Scope**: Two features — (1) serve an OpenAPI spec from the API, (2) watch filesystem for new databases matching configured globs.

---

## Feature 1: OpenAPI Spec Endpoint

### Goal

Expose a machine-readable API description so any client (credential broker, automation tools, scripts) can self-discover the API surface.

### Approach

Hand-written OpenAPI 3.1 YAML file, embedded in the binary via `//go:embed`, served as JSON at an unauthenticated endpoint.

### Spec file

**Location**: `api/openapi.yaml` (repo root-level `api/` directory, following Go project layout conventions).

**Embedded** into `internal/api/router.go` via `//go:embed ../../api/openapi.yaml`.

**Converted** from YAML to JSON once at startup (`yaml.v3` → `encoding/json`). Both packages are already in the dependency tree.

### Endpoint

| Method | Path | Auth | Content-Type |
|--------|------|------|--------------|
| GET | `/v1/openapi.json` | No | `application/json` |

Added to the unauthenticated routes alongside `/v1/health`.

### Spec coverage (OpenAPI 3.1)

**Endpoints documented**:

1. `GET /v1/health` — liveness probe, returns `{data: {status: "ok"|"degraded"}}`
2. `GET /v1/` — list databases with alias, path, status, modified_at
3. `GET /v1/version` — binary version string
4. `GET /v1/{db}/tables` — list table names in a database
5. `GET /v1/{db}/{table}` — query rows with filtering, pagination, sorting
6. `POST /v1/{db}/query` — SQL passthrough (SELECT/WITH only)
7. `GET /v1/openapi.json` — the spec itself

**Reusable components**:
- `DataEnvelope` — `{data: ...}` wrapper
- `ErrorEnvelope` — `{error: {code: string, message: string}}`
- `ErrorCode` enum — `not_found`, `unauthorized`, `bad_request`, `forbidden`, `rate_limited`, `internal_error`
- `DatabaseInfo` — `{alias, path, status, modified_at}`
- `QueryResult` — `{database, table?, count, rows}`
- Auth schemes: `ApiKeyHeader` (`X-API-Key`) and `BearerAuth` (`Authorization: Bearer`)

**Query parameters** for table endpoint:
- `limit` (integer, default 1000)
- `offset` (integer, default 0)
- `sort` (string, column name)
- `sort_desc` (boolean, default false)
- Additional query params treated as column=value filters (documented via `additionalProperties`)

**POST body** for SQL endpoint:
```json
{"sql": "SELECT ...", "params": [value1, value2]}
```

**Rate limiting**: Documented in API description and as 429 response on all endpoints.

### Files changed

| File | Change |
|------|--------|
| `api/openapi.yaml` | **New** — hand-written OpenAPI 3.1 spec |
| `internal/api/router.go` | Embed spec, add handler + unauthenticated route |

---

## Feature 2: Database Modified Timestamp

### Goal

Expose file mtime in the database listing so clients can detect stale data without querying.

### Change

Add `ModifiedAt *time.Time` field to `mdb.DBInfo`:

```go
type DBInfo struct {
    Alias      string     `json:"alias"`
    Path       string     `json:"path"`
    Status     string     `json:"status"`
    ModifiedAt *time.Time `json:"modified_at,omitempty"` // file mtime, nil if stat fails
}
```

Populated in `Pool.Databases()` via `os.Stat(path).ModTime()`. If stat fails (file temporarily unavailable on network share), the field is omitted rather than erroring.

### Files changed

| File | Change |
|------|--------|
| `internal/mdb/pool.go` | Add `ModifiedAt` to `DBInfo`, populate in `Databases()` |

---

## Feature 3: Glob Watcher

### Goal

Automatically discover and register new `.mdb`/`.accdb` files that match configured glob patterns, without requiring a service restart.

### Approach

Dual-mechanism watcher: fsnotify for instant detection + periodic poll as fallback. Both run concurrently and feed into the same registration path.

### Architecture

```
                    ┌─────────────┐
                    │   Watcher   │
                    │             │
              ┌─────┤  fsnotify   │  (watches glob base dirs for Create events)
              │     │             │
              │     │  poll loop  │  (re-expands globs every 30s)
              │     └─────────────┘
              │
              ▼
     ┌─────────────────┐
     │   Registrar      │  (interface: Register(DBConfig) error)
     │   (Pool)         │
     └─────────────────┘
              │
              ▼
     Database immediately queryable via API
```

### New package: `internal/watcher`

**Registrar interface** (implemented by Pool):
```go
type Registrar interface {
    Register(cfg mdb.DBConfig) error
    Aliases() []string
}
```

**Watcher struct**:
- Receives: list of glob-type `DatabaseConfig` entries + `Registrar` + logger
- On start: sets up fsnotify watches on each glob's base directory, starts poll ticker
- On new file detected (either mechanism):
  1. Check file extension matches glob pattern
  2. Derive alias using same logic as `config.expandGlobs()`
  3. Check alias not already registered (call `Registrar.Aliases()`)
  4. Call `Registrar.Register(DBConfig{Alias, Path, Driver})`
  5. Log addition with source (fsnotify or poll)
- Deduplication: both mechanisms may fire for the same file — alias check prevents double-registration

**fsnotify details**:
- Watch the base directory of each glob (longest path prefix before first wildcard)
- Filter `Create` events only
- Match against glob pattern before attempting registration
- If watcher setup fails (e.g., network share), log warning and rely on poll

**Poll details**:
- Every 30 seconds (constant, not configurable initially)
- Re-run `filepath.Glob()` for each glob pattern
- Compare results against `Registrar.Aliases()`
- Register any new matches

### Pool changes

Add `Register` method to `Pool`:

```go
func (p *Pool) Register(cfg DBConfig) error {
    p.mu.Lock()
    defer p.mu.Unlock()
    if _, exists := p.dbs[cfg.Alias]; exists {
        return nil // already registered
    }
    db, connStr, err := openDB(cfg.Path, cfg.Driver)
    if err != nil {
        return fmt.Errorf("open database %q (%s): %w", cfg.Alias, cfg.Path, err)
    }
    p.dbs[cfg.Alias] = db
    p.paths[cfg.Alias] = cfg.Path
    p.connStrs[cfg.Alias] = connStr
    return nil
}
```

Existing methods (`Get`, `Aliases`, `Databases`, `Close`) already use mutex — no changes needed.

### Watcher lifecycle

In `winservice/service.go`, after the HTTP server starts listening:

```
httpSrv.Serve(ln)  // async
watcher.Start(ctx) // blocks on goroutines until ctx cancelled
```

Shutdown: context cancellation stops poll ticker and fsnotify goroutine. `watcher.Stop()` closes the fsnotify watcher.

### What we explicitly don't do

- **No removal** of databases that disappear — ODBC queries fail naturally, surfaced as errors to clients
- **No config file watching** — only filesystem globs for new database files
- **No re-reading config.yaml** at runtime

### Files changed

| File | Change |
|------|--------|
| `internal/mdb/pool.go` | Add `Register(DBConfig) error` method |
| `internal/watcher/watcher.go` | **New** — Watcher struct, fsnotify + poll loop, Registrar interface |
| `internal/watcher/watcher_test.go` | **New** — unit tests with temp dir + mock registrar |
| `internal/winservice/service.go` | Wire up watcher after server start |

### New dependency

- `github.com/fsnotify/fsnotify` — pure Go, well-maintained, Windows support via ReadDirectoryChangesW

---

## Testing summary

| Test | Location | Type |
|------|----------|------|
| OpenAPI endpoint returns valid JSON, correct content-type, no auth | `internal/api/api_test.go` | Unit |
| Pool.Register adds new database, deduplicates | `internal/mdb/pool_test.go` | Unit |
| Watcher detects new file via poll, calls Register | `internal/watcher/watcher_test.go` | Unit |
| Watcher deduplicates across fsnotify + poll | `internal/watcher/watcher_test.go` | Unit |
| DBInfo.ModifiedAt populated from file stat | `internal/mdb/pool_test.go` | Unit |

---

## Files summary (all changes)

| File | Status |
|------|--------|
| `api/openapi.yaml` | New |
| `internal/api/router.go` | Modified |
| `internal/api/api_test.go` | Modified |
| `internal/mdb/pool.go` | Modified |
| `internal/watcher/watcher.go` | New |
| `internal/watcher/watcher_test.go` | New |
| `internal/winservice/service.go` | Modified |
