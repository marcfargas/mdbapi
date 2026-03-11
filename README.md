# mdbapi

A self-contained Windows service that exposes Microsoft Access databases (`.mdb`, `.accdb`) as a read-only REST API.

**One binary. No sidecar processes. No database. No framework.** Drop it on the machine that holds the Access files, point it at your databases, and start querying over HTTP.

## Features

- **5 clean endpoints** — health, list databases, list tables, query rows, SQL passthrough
- **Glob database config** — expose entire directories with `*.mdb` / `**\*.mdb` patterns
- **Read-only by design** — `ReadOnly=1` in ODBC connection string + SELECT-only enforcement
- **Multi-key auth** — zero-downtime key rotation without service restart
- **Auto-update** — checks GitHub Releases hourly, replaces binary, restarts via SCM
- **Windows service** — install/start/stop/uninstall via SCM; recovery actions configured automatically
- **Optional Tailscale tunnel** — build with `-tags tsnet` for in-process tsnet support
- **MIT licensed**

## Requirements

- Windows (amd64 or ARM64 via x64 emulation)
- [Microsoft Access Database Engine Redistributable 2016 (64-bit)](https://www.microsoft.com/en-us/download/details.aspx?id=54920) — free, ~50 MB

> **Architecture note**: if 32-bit Microsoft Office is installed, install the 64-bit ACE engine with the `/passive` flag, or [build a 32-bit binary](#building).

## Quick Start

```powershell
# 1. Download the latest release
$url = "https://github.com/marcfargas/mdbapi/releases/latest/download/mdbapi_windows_amd64.exe"
Invoke-WebRequest $url -OutFile mdbapi.exe

# 2. Create config directory and copy example config
New-Item -ItemType Directory -Path "C:\ProgramData\MDBService" -Force
# edit C:\ProgramData\MDBService\config.yaml (see configs/config.example.yaml)

# 3. Generate an API key and add it to config
.\mdbapi.exe keygen -add-to-config "C:\ProgramData\MDBService\config.yaml"

# 4. Set restrictive ACLs on the config directory
.\mdbapi.exe fixacl

# 5. Validate config and test DB connections
.\mdbapi.exe check

# 6. Install and start the service
.\mdbapi.exe install
.\mdbapi.exe start
```

## API Reference

All responses use a JSON envelope:
- Success: `{"data": {...}}`
- Error: `{"error": {"code": "...", "message": "..."}}`

Authentication:
- `Authorization: Bearer <key>`, or
- `X-API-Key: <key>`
- `GET /v1/health` is unauthenticated

### `GET /v1/health` (no auth)

```json
{
  "data": {
    "status": "ok",
    "version": "v0.1.0",
    "databases": { "total": 2, "ok": 2, "error": 0 }
  }
}
```

`status` is `"degraded"` if any database cannot be pinged.

### `GET /v1/` (auth required)

```json
{
  "data": {
    "databases": [
      {"alias": "customers", "path": "C:\\Data\\Customers.mdb", "status": "ok"},
      {"alias": "inventory", "path": "C:\\Data\\Inventory.mdb", "status": "ok"}
    ]
  }
}
```

### `GET /v1/{db}/tables` (auth required)

```json
{ "data": { "database": "inventory", "tables": ["Products", "Orders"] } }
```

### `GET /v1/{db}/{table}` (auth required)
Query params: `field=value` filters, `limit=N` (default 100, max from config), `offset=N`, `sort=col`, `sort_desc=true`

```json
{
  "data": {
    "database": "inventory",
    "table": "Products",
    "count": 2,
    "rows": [
      {"id": 1, "name": "Widget", "active": true, "created": "2024-06-15T12:00:00Z"}
    ]
  }
}
```

### `POST /v1/{db}/query` (auth required)
Body: `{"sql": "SELECT TOP 10 * FROM Products WHERE active = True", "params": []}`

Only `SELECT`/`WITH` queries are allowed; other statements return `403`.

```json
{ "data": { "database": "inventory", "count": 1, "rows": [...] } }
```

## JSON Type Mapping

| Access type | JSON |
|---|---|
| Yes/No | `true` / `false` |
| Number (Long Integer, Integer) | number |
| Number (Double, Single) | number |
| Date/Time | `"2024-06-15T12:00:00Z"` (RFC3339, UTC) |
| Text, Memo | string |
| OLE Object / Binary | base64 string |
| Null | `null` |

## Configuration

Copy `configs/config.example.yaml` to `C:\ProgramData\MDBService\config.yaml`.

Key settings:

```yaml
auth:
  keys:
    - "${MDBAPI_KEY}"          # env var — use mdbapi keygen to generate

databases:
  - alias: "inventory"
    path: 'C:\Data\Inventory.mdb'

  # Glob: expose all .mdb files in a directory
  - glob: 'C:\Data\*.mdb'

  # Recursive glob
  - glob: 'C:\Data\**\*.mdb'

api:
  max_rows: 1000               # hard cap per request

tunnel:
  provider: none               # none | tsnet (requires -tags tsnet build)
```

Glob alias derivation:
- Non-recursive glob (`*.mdb`): filename without extension, lowercased.
- Recursive glob (`**\*.mdb`): relative path from the glob base with path separators replaced by `_`.

**Setting the env var for the service:**

```powershell
# Via registry (persists across reboots):
$regPath = "HKLM:\SYSTEM\CurrentControlSet\Services\MDBRestService"
New-ItemProperty -Path $regPath -Name "Environment" -PropertyType MultiString `
  -Value @("MDBAPI_KEY=your-key-here") -Force
```

Or pass it through `sc.exe`:
```
sc config MDBRestService obj= "NT SERVICE\MDBRestService" password= ""
```

## CLI Commands

```
mdbapi run       [-config path]       Run in foreground (dev mode)
mdbapi install   [-config path]       Install Windows service
mdbapi uninstall [-config path]       Uninstall Windows service
mdbapi start     [-config path]       Start the service
mdbapi stop      [-config path]       Stop the service
mdbapi check     [-config path]       Validate config and test DB connections
mdbapi keygen    [-add-to-config]     Generate a new API key
mdbapi fixacl    [-dir path]          Set restrictive ACLs on config directory
mdbapi version                        Print version
```

## Building

```bash
# Standard build (no tunnel)
make build-amd64        # → dist/mdbapi_windows_amd64.exe

# With Tailscale tsnet tunnel support (~20 MB larger)
make build-amd64-tsnet  # → dist/mdbapi_windows_amd64_tsnet.exe

# 32-bit (required if 32-bit Office is installed)
GOOS=windows GOARCH=386 go build -o dist/mdbapi_windows_386.exe ./cmd/mdbapi

# All targets
make build-all
```

## Development

```bash
# Run unit tests (no ODBC required, works on any OS)
go test ./...

# Run integration tests (requires Windows + 64-bit ACE driver)
# Cross-compile probe, copy to Windows machine, run against testdata/
make build-probe
dist\probe_windows_amd64.exe -dir testdata\

# Lint
make lint
```

## Auto-Update

When `updater.enabled: true`, mdbapi checks for new GitHub Releases hourly.

On update:
1. Downloads new binary to temp path
2. Verifies SHA256 against `checksums.txt` in the release
3. Renames running binary to `.mdb.old`
4. Writes new binary
5. Gracefully shuts down HTTP (drains in-flight requests, 15s timeout)
6. Closes database connections (prevents stale `.ldb` lock files)
7. Exits with code 1 — SCM recovery action restarts after 5 seconds

To avoid GitHub API rate limits (60 req/h unauthenticated), set `updater.github_token`.

## Security

- **Service runs as `NT SERVICE\MDBRestService`** — no admin rights, no network logon
- **Config directory ACLs** — `SYSTEM` and `Administrators` only (`mdbapi fixacl`)
- **Read-only ODBC** — `ReadOnly=1` prevents writes at the driver level
- **SELECT-only SQL** — passthrough endpoint rejects non-SELECT statements
- **Multi-key auth** — 500ms fail delay on bad keys; all failures logged with source IP
- **Minimum key entropy** — 32 characters required; `mdbapi keygen` generates cryptographically random keys
- **No TLS by default** — bind to `127.0.0.1` (loopback only). For LAN access, add `server.tls_cert` / `server.tls_key`; for internet access, use [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) or the tsnet build

## License

MIT — see [LICENSE](LICENSE).
