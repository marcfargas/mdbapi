# Building a self-contained Go Windows service for MDB access

A single Go binary can read Microsoft Access databases, expose a REST API, tunnel to the internet, and self-update — all running as a Windows service. **The recommended stack is `alexbrainman/odbc` for MDB access (no CGo on Windows), Tailscale `tsnet` for in-process tunneling, `creativeprojects/go-selfupdate` for GitHub-based auto-update, and `kardianos/service` for Windows service lifecycle.** This combination produces one `.exe` with no external processes or sidecars. The only runtime dependency is the free Microsoft Access Database Engine Redistributable on the target machine.

---

## Reading Access databases without CGo

Three viable approaches exist for reading MDB/ACCDB files from Go on Windows. Each trades off between self-containment and functionality.

### `github.com/alexbrainman/odbc` — the recommended choice

This library calls Windows' `odbc32.dll` directly via `syscall` — **no CGo, no C compiler needed** on Windows. It produces a clean, standalone `.exe` and implements the standard `database/sql` interface, giving you full SQL query capability including JOINs, WHERE clauses, and parameterized queries.

```go
import (
    "database/sql"
    _ "github.com/alexbrainman/odbc"
)

connStr := `DRIVER={Microsoft Access Driver (*.mdb, *.accdb)};DBQ=C:\data\mydb.mdb`
db, err := sql.Open("odbc", connStr)
```

The library specifically detects Access connections and adjusts for Access-specific quirks. It supports both `.mdb` (Jet) and `.accdb` (ACE) formats. To list tables, query the system table:

```go
rows, err := db.Query("SELECT Name FROM MSysObjects WHERE Type=1 AND Flags=0")
```

**The caveat**: the target machine needs the **Microsoft Access Database Engine Redistributable** installed (free, ~50 MB, supports silent install via `/quiet`). This provides the ODBC driver `Microsoft Access Driver (*.mdb, *.accdb)`. The binary itself is self-contained — but the Windows ODBC infrastructure must be present. With **388 GitHub stars** and 278 dependents, this is the most battle-tested Go ODBC driver.

**Known gotcha**: parameterized integer inserts can fail with "COUNT Field incorrect" — cast integers to `float64` when using `db.Exec` with parameters.

### `github.com/mattn/go-adodb` — COM-based alternative

Also CGo-free on Windows, this library uses COM/OLE automation via `github.com/go-ole/go-ole` to create an `ADODB.Connection` object — the same technology VBScript uses. Connection string format differs:

```go
connStr := `Provider=Microsoft.ACE.OLEDB.12.0;Data Source=C:\data\mydb.mdb;`
db, err := sql.Open("adodb", connStr)
```

Same external dependency (ACE Redistributable), but this path uses OLE DB rather than ODBC. It's **151 stars**, maintained by mattn (prolific Go contributor). The downside: COM threading can cause crashes — the README suggests adding `import _ "runtime/cgo"` as a workaround. This makes `alexbrainman/odbc` the safer choice for a long-running Windows service.

### `github.com/arch-mage/mdb` — pure Go, zero dependencies

The only option requiring **nothing** installed on the target machine. It parses the MDB binary format directly in pure Go. However, it only supports **Jet3 format** (Access 97/2000 `.mdb` files), has **no SQL engine** (just raw table iteration), and does not support `.accdb` or Jet4 files. Suitable only for extracting data from legacy databases:

```go
tables, _ := mdb.Tables(file)
rows, _ := mdb.Rows(file, "MyTable")
for { row, err := rows.Next(); /* ... */ }
```

**Verdict**: use `alexbrainman/odbc` for production. The Access ODBC driver requirement is manageable — bundle the redistributable installer or document it as a prerequisite.

---

## REST API with Go 1.22 stdlib routing

Go 1.22 introduced method-based routing and path parameters natively in `http.ServeMux`, eliminating the need for an external router for these four endpoints.

```go
func setupRoutes(cfg *config.Config) http.Handler {
    mux := http.NewServeMux()

    mux.HandleFunc("GET /{$}", handleListDatabases)        // exact match on /
    mux.HandleFunc("GET /{db}/tables", handleListTables)
    mux.HandleFunc("GET /{db}/{table}", handleQueryRows)
    mux.HandleFunc("POST /{db}/query", handleSQLQuery)

    var handler http.Handler = mux
    handler = recoveryMiddleware(handler)
    handler = authMiddleware(cfg.Auth.APIKey, handler)
    handler = loggingMiddleware(handler)
    return handler
}
```

Path parameters are extracted with `r.PathValue("db")` and `r.PathValue("table")`. The `{$}` suffix on `GET /{$}` ensures exact root matching rather than prefix matching. Filter parameters on the query endpoint come from `r.URL.Query()` — for example, `GET /inventory/Products?category=widgets&limit=100` maps naturally.

The SQL passthrough endpoint accepts a JSON body:

```go
func handleSQLQuery(w http.ResponseWriter, r *http.Request) {
    dbAlias := r.PathValue("db")
    var req struct {
        SQL    string        `json:"sql"`
        Params []interface{} `json:"params"`
    }
    json.NewDecoder(r.Body).Decode(&req)
    // Look up *sql.DB from config by alias, execute req.SQL with req.Params
    rows, err := dbConn.QueryContext(r.Context(), req.SQL, req.Params...)
    // Marshal rows to JSON response
}
```

Middleware wraps as nested functions: `logging(auth(recovery(mux)))`. If you later want cleaner middleware stacking and route grouping, **`github.com/go-chi/chi/v5`** is the lightest upgrade — it's 100% `net/http` compatible and adds `r.Use()`, `r.Route()`, and `r.Group()`.

---

## Embedded tunneling — tsnet wins decisively

Seven tunneling solutions were evaluated for in-process embedding. Only three can be imported as Go packages without shelling out. **Tailscale `tsnet` is the clear winner** — zero infrastructure to manage, one function call for a publicly reachable HTTPS endpoint.

### Tailscale `tsnet` — best option (no server needed)

The `tailscale.com/tsnet` package embeds a full Tailscale node in-process, complete with a userland TCP/IP stack (WireGuard). No `tailscaled` daemon, no sidecar, no VPS. Authentication uses pre-auth keys or OAuth client credentials.

```go
import "tailscale.com/tsnet"

srv := &tsnet.Server{
    Hostname: "mdb-service",
    Dir:      "./tsnet-state",       // persists node identity
    AuthKey:  os.Getenv("TS_AUTHKEY"),
}
defer srv.Close()

// Expose to public internet via Tailscale Funnel
ln, err := srv.ListenFunnel("tcp", ":443")
if err != nil { log.Fatal(err) }

http.Serve(ln, setupRoutes(cfg))
```

`ListenFunnel` returns a standard `net.Listener` with a publicly reachable URL like `https://mdb-service.tail1234.ts.net`, complete with **automatic TLS certificates**. For tailnet-only access (no public exposure), use `srv.Listen("tcp", ":80")` instead. Binary size increases by **~20 MB** — acceptable for a Windows service.

**Caveats**: requires a free Tailscale account, Funnel must be enabled in admin console, auth keys expire after 90 days (use OAuth client credentials for long-lived services), and Funnel only supports ports **443, 8443, and 10000**.

### Chisel — best self-hosted alternative

If you need full infrastructure control, `github.com/jpillora/chisel` provides clean client embedding (**15.7k stars**). You must run a chisel server on a VPS, but the client is trivial to embed:

```go
import chclient "github.com/jpillora/chisel/client"

config := &chclient.Config{
    Server:           "https://my-chisel-server.com:8443",
    Remotes:          []string{"R:9090:127.0.0.1:8080"},
    Auth:             "user:password",
    KeepAlive:        25 * time.Second,
    MaxRetryCount:    -1,
    MaxRetryInterval: 5 * time.Minute,
}
c, _ := chclient.NewClient(config)
c.Start(context.Background())
```

This creates a reverse tunnel from the server's port 9090 to the local API on 8080. Binary overhead is only **~5-8 MB**. Portainer uses exactly this pattern in production.

### What doesn't work

**Cloudflare `cloudflared`** — internal packages are tightly coupled to the CLI; not designed as a library. **Bore** — written in Rust, no Go bindings. **Rathole** — also Rust. **pgrok** — CLI only, no exported library API. **frp** can technically be embedded via `github.com/fatedier/frp/client` (or the simpler `github.com/Silicon-Ally/frpembed` wrapper), but the configuration API is significantly more complex than chisel's and still requires a self-hosted frps server.

---

## Auto-update from GitHub Releases

The recommended library handles the entire update lifecycle in ~20 lines of code, including the Windows-specific binary replacement dance.

### `github.com/creativeprojects/go-selfupdate` — complete solution

This library (a maintained fork of `rhysd/go-github-selfupdate`) integrates GitHub Releases API detection, semver comparison, OS/arch asset matching, checksum validation, and atomic binary replacement:

```go
import selfupdate "github.com/creativeprojects/go-selfupdate"

func checkAndUpdate(currentVersion string) (bool, error) {
    updater, _ := selfupdate.NewUpdater(selfupdate.Config{
        Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"},
    })

    latest, found, err := updater.DetectLatest(context.Background(),
        selfupdate.NewRepositorySlug("yourorg", "mdb-service"))
    if err != nil || !found { return false, err }

    if latest.LessOrEqual(currentVersion) { return false, nil }

    exe, _ := selfupdate.ExecutablePath()
    return true, updater.UpdateTo(context.Background(), latest, exe)
}
```

`DetectLatest` calls the GitHub API (`GET /repos/{owner}/{repo}/releases/latest`), parses the tag as semver, and matches asset names to `runtime.GOOS`/`runtime.GOARCH` using goreleaser naming conventions (e.g., `mdb-service_windows_amd64.exe`). It downloads `checksums.txt` from the release assets and verifies SHA256 before applying.

**The critical Windows insight**: you **can rename** a running `.exe` on Windows — you just can't delete or overwrite it. The library internally writes the new binary as `.new`, renames the running binary to `.old` (which succeeds), renames `.new` to the original name, then hides the `.old` file since it can't be deleted while the process runs.

For comparison, `github.com/minio/selfupdate` handles only the low-level binary replacement (you provide an `io.Reader`), while `github.com/inconshreveable/go-update` has been **abandoned since 2016** and should not be used.

### Restarting a Windows service after update

A Windows service cannot directly restart itself through the SCM. The cleanest pattern uses **SCM Recovery Actions**: configure the service to automatically restart on failure, then exit with a non-zero code after applying the update.

```go
// During service installation, configure recovery:
recoveryActions := []mgr.RecoveryAction{
    {Type: mgr.ServiceRestart, Delay: 5 * time.Second},
    {Type: mgr.ServiceRestart, Delay: 5 * time.Second},
    {Type: mgr.ServiceRestart, Delay: 5 * time.Second},
}
s.SetRecoveryActions(recoveryActions, 3600) // reset counter after 1 hour
s.SetRecoveryActionsOnNonCrashFailures(true) // trigger on clean exit(1) too
```

Then in the service's update check loop, after a successful update: return from `Execute()` with exit code 1. The SCM sees the non-zero exit, waits 5 seconds, and starts the service again — now running the new binary. On startup, clean up any stale `.old` files from previous updates.

---

## Windows service with dual-mode execution

### `github.com/kardianos/service` — recommended over raw `x/sys/windows/svc`

With **4,700+ stars** and 1,359 importers, this cross-platform wrapper handles service install/uninstall, console-vs-service detection, and SCM lifecycle with a simpler API than the official `golang.org/x/sys/windows/svc` package (which it uses internally).

```go
import "github.com/kardianos/service"

type program struct {
    httpServer *http.Server
    tsServer   *tsnet.Server
    updateStop chan struct{}
}

func (p *program) Start(s service.Service) error {
    // Start must not block
    go p.run()
    return nil
}

func (p *program) run() {
    // 1. Open MDB connections from config
    // 2. Start HTTP server
    // 3. Start tunnel (tsnet)
    // 4. Start update checker ticker
}

func (p *program) Stop(s service.Service) error {
    close(p.updateStop)
    ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
    defer cancel()
    p.httpServer.Shutdown(ctx)
    p.tsServer.Close()
    return nil
}
```

The same binary works as both a console app (for development) and a Windows service. `kardianos/service` auto-detects the context — `service.Interactive()` returns `true` when running from a terminal. CLI commands handle lifecycle management:

```
mdb-service.exe -service install -config C:\path\config.yaml
mdb-service.exe -service start
mdb-service.exe -service stop
mdb-service.exe -service uninstall
mdb-service.exe -config config.yaml    # console mode for dev
```

---

## Configuration and project structure

### YAML configuration

YAML via `gopkg.in/yaml.v3` is the standard choice for Go service configuration — it handles nested structures cleanly and natively parses `time.Duration` values.

```yaml
service:
  name: "MDBRestService"
  display_name: "MDB REST API Service"
  description: "Exposes Access MDB files via REST API"
  log_file: "C:\\ProgramData\\MDBService\\service.log"

server:
  listen: "127.0.0.1:8080"
  read_timeout: 30s
  write_timeout: 30s

auth:
  api_key: "your-secret-api-key-here"

databases:
  - alias: "inventory"
    path: "C:\\Data\\Inventory.mdb"
    readonly: true
  - alias: "customers"
    path: "C:\\Data\\Customers.accdb"
    readonly: true

tunnel:
  provider: "tsnet"            # tsnet | chisel | none
  tsnet:
    hostname: "mdb-service"
    auth_key: "tskey-auth-..."
    state_dir: "C:\\ProgramData\\MDBService\\tsnet"
    funnel: true
  chisel:
    server: "https://tunnel.example.com:8443"
    auth: "user:password"
    remote: "R:9090:127.0.0.1:8080"

updater:
  enabled: true
  github_repo: "myorg/mdb-service"
  check_interval: 1h
  auto_apply: true
```

### Project directory layout

```
mdb-service/
├── cmd/
│   └── mdb-service/
│       └── main.go               # Entry point: flags, config load, service run
├── internal/
│   ├── config/
│   │   └── config.go             # Config structs + Load()
│   ├── api/
│   │   ├── router.go             # Route setup with Go 1.22 ServeMux
│   │   ├── handlers.go           # 4 endpoint handlers
│   │   ├── middleware.go          # Auth, logging, recovery, CORS
│   │   └── response.go           # JSON helpers, error types
│   ├── mdb/
│   │   ├── pool.go               # Connection pool keyed by alias
│   │   ├── query.go              # Generic row→map scanning
│   │   └── schema.go             # Table/column listing via MSysObjects
│   ├── tunnel/
│   │   └── tunnel.go             # tsnet/chisel abstraction
│   ├── updater/
│   │   └── updater.go            # GitHub release check + apply
│   └── winservice/
│       └── service.go            # kardianos/service Program impl
├── configs/
│   └── config.example.yaml
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

---

## Complete dependency list and gotchas

### Import paths for `go.mod`

| Component | Package | Purpose |
|-----------|---------|---------|
| MDB access | `github.com/alexbrainman/odbc` | ODBC driver via syscall (no CGo) |
| Tunnel | `tailscale.com/tsnet` | In-process Tailscale node |
| Tunnel alt | `github.com/jpillora/chisel/client` | Embeddable reverse tunnel client |
| Auto-update | `github.com/creativeprojects/go-selfupdate` | Full GitHub Releases update flow |
| Service | `github.com/kardianos/service` | Cross-platform service wrapper |
| Config | `gopkg.in/yaml.v3` | YAML config parsing |
| SCM management | `golang.org/x/sys/windows/svc/mgr` | Recovery action configuration |

### Critical gotchas to watch for

**ODBC driver architecture mismatch** is the most common deployment failure. If you compile a 64-bit Go binary, you need the **64-bit** Access Database Engine Redistributable. The 32-bit and 64-bit versions cannot coexist with a matching Office installation — if 32-bit Office is installed, you must compile your Go binary as 32-bit (`GOARCH=386`) or install the 64-bit engine with `/passive` flag.

**tsnet state directory** must be persistent and writable. On first run, the node registers with Tailscale and stores its identity in `Dir`. If the state directory is lost, the node gets a new identity and the old Funnel DNS entry becomes orphaned. Use `C:\ProgramData\MDBService\tsnet\` for a Windows service.

**Service account permissions** matter. The default `LocalSystem` account can access local files but may not access network shares (UNC paths like `\\server\share\db.mdb`). If MDB files live on network shares, configure the service to run under a domain account with appropriate permissions.

**Concurrent MDB access** is limited. Access databases use file-level locking. Multiple simultaneous queries through the ODBC driver work (the driver handles locking internally), but heavy concurrent writes can cause "database locked" errors. For a read-heavy REST API this is rarely an issue — set `ReadOnly=1` in the connection string when writes aren't needed.

**Binary size** will be approximately **30-55 MB** depending on tunnel choice: ~10 MB base Go binary + ~20 MB for tsnet + overhead. Compress with `go build -ldflags="-s -w"` and optionally UPX to reduce by 60-70%.

## Conclusion

The architecture centers on a pragmatic tradeoff: accepting the Access ODBC driver as a runtime dependency (unavoidable for full SQL support) while making everything else fully self-contained. **tsnet is the standout discovery** — it eliminates the need to operate tunnel infrastructure entirely, turning what's usually a multi-component deployment into a single binary that registers itself on a Tailscale network and optionally exposes itself to the public internet via Funnel. The SCM recovery action pattern for self-update restarts is cleaner than the helper-process alternative and requires no additional binaries. Build with `GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.Version=1.0.0" ./cmd/mdb-service/` and the result is one `.exe` that installs itself, serves Access data over HTTPS, and keeps itself updated.