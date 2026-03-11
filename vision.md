# mdbapi — Vision

## Problem

Microsoft Access databases (`.mdb`, `.accdb`) are ubiquitous in small businesses: decades of
data locked in a format that has no native network interface, no REST API, and no path to
modern tooling. Extracting data requires either opening Access on the same machine or
deploying a heavy middleware stack.

**There is no lightweight, self-contained bridge from a Windows machine running Access data
to the rest of the world.**

## Goal

A single Windows executable (`mdbapi.exe`) that:

1. Reads MDB/ACCDB files via standard ODBC
2. Exposes them as a structured REST/JSON API (read + SQL passthrough)
3. Optionally tunnels itself to the internet without infrastructure
4. Auto-updates from GitHub Releases
5. Runs as a proper Windows service

Target users: developers and IT staff who need to integrate legacy Access data into modern
applications without replacing the source system.

MIT license. FOSS.

## Current State

- `design.md`: full architecture research document (stack decided)
- `vision.md`: this document
- No code yet

## Stack (Locked In)

| Component       | Package                                    | Rationale |
|-----------------|--------------------------------------------|-----------|
| MDB access      | `github.com/alexbrainman/odbc`             | No CGo, full SQL, battle-tested |
| REST layer      | Go 1.22 stdlib `net/http` ServeMux         | Method routing + path params built-in |
| Auto-update     | `github.com/creativeprojects/go-selfupdate`| Full lifecycle incl. Windows binary-replace dance |
| Service wrapper | `github.com/kardianos/service`             | Cross-platform SCM wrapper, 4.7k stars |
| Config          | `gopkg.in/yaml.v3`                         | Standard Go service config |

## Tunnel (Open Question — See Risks)

The tunnel component is **not locked in**. Three candidates:

### A. Tailscale `tsnet` (in-process node)
- Zero infrastructure: Tailscale handles everything
- `ListenFunnel` → public HTTPS URL automatically
- Requires Tailscale account + auth key management
- ~20 MB binary overhead
- Auth keys expire every 90 days (OAuth client credentials mitigate this)

### B. Chisel (self-hosted reverse tunnel)
- ~5-8 MB binary overhead
- Requires user to run a `chisel server` on a VPS
- Clean Go embedding via `github.com/jpillora/chisel/client`
- No account required; full infrastructure control

### C. No built-in tunnel ("local only")
- Zero overhead, zero dependencies
- Users deploy their own reverse proxy (nginx, Cloudflare Tunnel, etc.)
- Simplest codebase; tunnel is out of scope
- Tunnel stays pluggable via config: `tunnel.provider: none`

**Decision needed from design review.** Leaning toward C + optional A/B as compile-time or
config-selectable modules to keep the default binary minimal.

## Design

### API Surface (4 endpoints)

```
GET  /                        → list all configured database aliases
GET  /{db}/tables             → list tables in the given database
GET  /{db}/{table}            → query rows (filter params: field=val, limit=N, offset=N)
POST /{db}/query              → SQL passthrough { "sql": "...", "params": [...] }
```

Authentication: bearer token (API key in config, passed as `Authorization: Bearer <key>`
or `X-API-Key: <key>` header).

### Configuration (YAML)

```yaml
service:
  name: "MDBRestService"
  log_file: "C:\\ProgramData\\MDBService\\service.log"

server:
  listen: "127.0.0.1:8080"

auth:
  api_key: "${MDBAPI_KEY}"    # env var substitution

databases:
  - alias: "inventory"
    path: "C:\\Data\\Inventory.mdb"
    readonly: true

tunnel:
  provider: none              # none | tsnet | chisel

updater:
  enabled: true
  github_repo: "yourorg/mdbapi"
  check_interval: 1h
```

### Service CLI

```
mdbapi.exe install   [-config path]   # install Windows service
mdbapi.exe uninstall
mdbapi.exe start / stop
mdbapi.exe run       [-config path]   # foreground / dev mode
mdbapi.exe check     [-config path]   # validate config + test DB connections
mdbapi.exe version                    # print version
```

### Project Layout

```
mdbapi/
├── cmd/mdbapi/main.go
├── internal/
│   ├── api/          # router, handlers, middleware, response helpers
│   ├── config/       # YAML config structs + loader
│   ├── mdb/          # ODBC pool, query execution, schema listing
│   ├── tunnel/       # tunnel abstraction (provider interface)
│   ├── updater/      # GitHub release check + apply
│   └── winservice/   # kardianos/service integration
├── configs/config.example.yaml
├── testdata/         # symlink or submodule: github.com/mdbtools/mdbtestdata
├── Makefile
├── LICENSE           # MIT
└── README.md
```

### Cross-Compilation (Windows ARM dev machine)

Development happens on **Windows ARM** (e.g., Snapdragon X series).
Primary release target: `GOOS=windows GOARCH=amd64`.
Secondary target: `GOOS=windows GOARCH=arm64` (native ARM64 Windows).

```makefile
build-amd64:
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.Version=$(VERSION)" \
	    -o dist/mdbapi_windows_amd64.exe ./cmd/mdbapi

build-arm64:
	GOOS=windows GOARCH=arm64 go build -ldflags="-s -w -X main.Version=$(VERSION)" \
	    -o dist/mdbapi_windows_arm64.exe ./cmd/mdbapi
```

`alexbrainman/odbc` uses `syscall` (not CGo) so cross-compilation works natively.
The ODBC call itself resolves at runtime against the target machine's `odbc32.dll`.

### Test Data

Integration tests use `github.com/mdbtools/mdbtestdata` (public CC-licensed MDB samples).
Added as a Git submodule at `testdata/`. Unit tests mock the `database/sql` interface.

### Release Automation (GitHub Actions)

```
push tag v*  →  goreleaser  →  GitHub Release
                               ├── mdbapi_windows_amd64.exe
                               ├── mdbapi_windows_arm64.exe
                               └── checksums.txt
```

`go-selfupdate` matches assets by goreleaser naming convention.
Publish via **Trusted Publishers / OIDC** — no NPM_TOKEN (n/a, Go binary release).

## Phases

### Phase 0 — Skeleton (1-2 days)
- `go mod init`, package layout, MIT license
- Config loading with env var substitution
- ODBC connection pool (open/close/ping)
- `mdbapi check` command (validate config + test connections)
- Test submodule + basic integration test

### Phase 1 — Core API (2-3 days)
- All 4 REST endpoints with Go 1.22 routing
- Auth middleware (bearer + X-API-Key)
- Logging middleware (structured, w/ request IDs)
- Recovery middleware (panic → 500)
- Row → JSON serialization (handle Access type quirks: date, boolean, OLE)
- Integration tests against testdata MDBs

### Phase 2 — Service Lifecycle (1 day)
- `kardianos/service` integration
- Install / uninstall / start / stop CLI
- Recovery actions for SCM (restart on failure for auto-update)
- Structured log file rotation (lumberjack or stdlib)

### Phase 3 — Auto-Update (1 day)
- `go-selfupdate` integration
- Background ticker
- SCM recovery-based restart after update
- Version endpoint (`GET /version` → `{ "version": "1.2.3" }`)

### Phase 4 — Tunnel (0.5-1 day)
- Provider interface + `none` implementation
- Optional chisel or tsnet implementation (TBD by design review)

### Phase 5 — Release & Docs (1 day)
- goreleaser config
- GitHub Actions CI/CD
- README with install instructions, config reference
- CHANGELOG

## Constraints

- Windows-only runtime (builds on any platform; runs on Windows amd64/arm64)
- External dependency: Microsoft Access Database Engine Redistributable (free)
- No CGo in the binary — `alexbrainman/odbc` uses syscall
- Go 1.22+ (stdlib routing)
- Single binary deliverable

## Risks

1. **ODBC architecture mismatch**: 64-bit binary needs 64-bit ACE driver. If Office 32-bit is
   installed, users must compile 32-bit or use /passive flag. Mitigation: document clearly;
   add `mdbapi check` that validates driver architecture.

2. **Tunnel choice affects deployment complexity**: tsnet adds Tailscale account requirement
   (unacceptable for some users); chisel requires VPS; none leaves users on their own.
   Mitigation: ship "none" default + document Cloudflare Tunnel as recommended free option.

3. **Concurrent write access**: Access file-level locking limits write concurrency.
   Mitigation: default `readonly: true` in all config examples; warn on write-mode startup.

4. **ARM64 + ODBC**: `odbc32.dll` is present on ARM64 Windows (emulated or native) but
   the Access ODBC driver redistributable may only provide x64. Validate in Phase 0.

5. **Binary size**: tsnet adds ~20MB. With `chisel` it's ~30-40MB total; with `none` it's
   ~10-15MB. Prefer minimal default.

## Open Questions

1. **Tunnel default**: ship with `none` and doc Cloudflare Tunnel, or bundle tsnet/chisel?
2. **SQL passthrough safety**: whitelist SELECT-only, or allow arbitrary SQL (inc. writes)?
   (Config: `sql_passthrough: readonly | any`)
3. **Connection pool size**: how many concurrent ODBC connections per DB? Access typically
   handles 5-10 concurrent readers without degradation.
4. **Config hot-reload**: reload DB list on SIGHUP without restarting service?
5. **ARM64 ODBC driver availability**: needs validation on actual ARM64 Windows machine.
