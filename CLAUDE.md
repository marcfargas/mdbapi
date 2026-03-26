# mdbapi

REST API exposing Microsoft Access databases (.mdb/.accdb) over HTTP.
Single Go binary, runs as a Windows service, **read-only by design**.

## Quick reference

```bash
# Unit tests (no ODBC needed, runs on any platform)
make test                    # or: go test ./... -short -count=1

# Lint
make lint                    # or: go vet ./...

# Build (cross-compile from any OS)
make build-amd64             # standard
make build-amd64-tsnet       # with embedded Tailscale (+20 MB)
make build-all               # amd64 + arm64 + tsnet + probe
```

## Project layout

```
cmd/
  mdbapi/         CLI + Windows service entry point
  integration/    Integration test binary (needs Windows + ACE driver)
  probe/          ODBC driver probe tool
internal/
  api/            HTTP handlers, middleware, router, response helpers
  config/         YAML config loading, validation, glob expansion
  mdb/            ODBC pool, query builder, schema cache, type normalization
  tunnel/         Pluggable tunnel provider (none | tsnet)
  updater/        Auto-update from GitHub Releases / CI artifacts
  watcher/        Filesystem watcher for dynamic database discovery
  winservice/     SCM integration, ACL management, logging
api/              OpenAPI spec (openapi.yaml) + embed helper
configs/          Example config (config.example.yaml)
ci/               CI workflow support files
```

## Architecture

- **Read-only by design**: SQL validator rejects anything not SELECT/WITH → 403. `fixacl` sets NTFS read-only ACLs. No ODBC write path.
- **Tunnel as Provider interface**: `tunnel.Provider` abstracts the listener. `none` = plain TCP, `tsnet` = embedded Tailscale. Build tag `-tags tsnet` controls inclusion.
- **Glob-based DB discovery**: Config supports `glob: 'C:\Data\**\*.mdb'` → auto-expands to alias+path entries at load time.
- **Auto-update**: Two channels — `release` (GitHub Releases + go-selfupdate) and `develop` (CI artifacts). Binary hot-swap with SCM restart.
- **Build-tag stubs**: `driver_stub.go` / `catalog_stub.go` provide no-op implementations for non-Windows builds so unit tests compile everywhere.
- **OpenAPI spec**: Hand-written `api/openapi.yaml`, embedded in binary, served as JSON at `GET /v1/openapi.json` (unauthenticated).
- **Glob watcher**: `internal/watcher/` monitors filesystem for new databases matching configured globs. Dual mechanism: fsnotify for instant detection + periodic poll (30s) as fallback. New databases registered at runtime without restart.

## Build & test

- **Go 1.26+** required
- **CGO_ENABLED=0** for standard builds (ODBC uses syscall, not cgo)
- Unit tests run cross-platform with `-short` flag
- Integration tests require Windows + 64-bit ACE driver — **CI only** (GitHub Actions matrix: ACE 2016, ACE 2010, M365 Runtime)
- Version injected via `-ldflags -X main.Version=...`

## Key dependencies

| Package | Purpose |
|---------|---------|
| `alexbrainman/odbc` | ODBC driver for Access via syscall |
| `kardianos/service` | Windows service (SCM) integration |
| `creativeprojects/go-selfupdate` | Auto-update from GitHub Releases |
| `tailscale.com` | Embedded Tailscale node (tsnet build tag) |
| `lumberjack.v2` | Log rotation |
| `fsnotify/fsnotify` | Filesystem event notifications for glob watcher |

## CI/CD

GitHub Actions on `develop` and `main`:
1. Unit tests — `go vet` + `go test -short -race` on Ubuntu
2. Cross-compile — windows/amd64, windows/386, + tsnet variants
3. Integration tests — 3 matrix jobs on `windows-latest` (ACE 2016, ACE 2010, M365)

Releases via GoReleaser (standard + tsnet archives). Tag with `make release VERSION=v0.x.x`.

## Conventions

- Conventional commits: `feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`
- Work on `develop`, releases from `main`
- `go test ./... -short` must pass before push
- Config lives at `C:\ProgramData\MDBService\config.yaml` on deployed machines
- Config supports `${ENV_VAR}` substitution for secrets
