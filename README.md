# mdbapi

REST API for Microsoft Access databases on Windows.

One binary. Install it on the machine that has the `.mdb`/`.accdb` files, point it at them, query over HTTP. Runs as a Windows service.

## Install

```powershell
# Latest release
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/marcfargas/mdbapi/develop/install.ps1)))

# Latest develop build
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/marcfargas/mdbapi/develop/install.ps1))) -Develop

# With Tailscale tunnel support (access across network boundaries)
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/marcfargas/mdbapi/develop/install.ps1))) -Develop -Tsnet
```

Requires the [Microsoft Access Database Engine](https://www.microsoft.com/en-us/download/details.aspx?id=54920) (64-bit, free). ACE 2016 for modern files, ACE 2010 if you also need Access 97 support.

> If 32-bit Office is installed, run the ACE installer with `/passive`.

## Setup

```powershell
# Generate API key and add to config
mdbapi keygen -add-to-config

# Lock down config directory permissions
mdbapi fixacl

# Validate config and test database connections
mdbapi check

# Install and start the Windows service
mdbapi install
Start-Service MDBRestService
```

Config lives at `C:\ProgramData\MDBService\config.yaml`:

```yaml
server:
  listen: "127.0.0.1:8080"

auth:
  keys:
    - "your-api-key-here"

databases:
  - alias: inventory
    path: 'C:\Data\Inventory.mdb'

  # Or expose an entire directory
  - glob: 'C:\Data\*.mdb'

  # Recursive
  - glob: 'C:\Data\**\*.mdb'
```

## Test it

After starting the service, test from PowerShell:

```powershell
# Health check (no auth)
Invoke-RestMethod http://localhost:8080/v1/health

# List databases
$key = "your-api-key"
$headers = @{ "X-API-Key" = $key }
Invoke-RestMethod http://localhost:8080/v1/ -Headers $headers

# List tables
Invoke-RestMethod http://localhost:8080/v1/inventory/tables -Headers $headers

# Query rows
Invoke-RestMethod "http://localhost:8080/v1/inventory/Products?limit=10" -Headers $headers

# SQL passthrough
Invoke-RestMethod http://localhost:8080/v1/inventory/query -Method POST -Headers $headers `
  -ContentType "application/json" -Body '{"sql": "SELECT TOP 10 * FROM Products"}'
```

## API

Auth via `Authorization: Bearer <key>` or `X-API-Key: <key>`. All responses: `{"data": ...}` or `{"error": {"code": "...", "message": "..."}}`.

| Endpoint | Auth | Description |
|---|---|---|
| `GET /v1/health` | No | Status, version, database health counts |
| `GET /v1/` | Yes | List databases with alias, path, status |
| `GET /v1/{db}/tables` | Yes | List tables in a database |
| `GET /v1/{db}/{table}` | Yes | Query rows. Params: `field=value`, `limit`, `offset`, `sort`, `sort_desc` |
| `POST /v1/{db}/query` | Yes | SQL passthrough. Body: `{"sql": "SELECT ..."}`. Only SELECT/WITH allowed (403 otherwise) |

## Security

### Read-only by design

- SQL layer rejects anything that isn't `SELECT` or `WITH` → HTTP 403
- `mdbapi fixacl` sets NTFS read-only ACLs on your database files
- No ODBC write path exists in the code

### HTTP hardening (safe for public exposure via Tailscale Funnel)

- **Constant-time key comparison** — API keys are validated using SHA-256 hashing + `crypto/subtle.ConstantTimeCompare` to prevent timing attacks
- **Per-IP rate limiting** — token bucket (default 10 req/s, burst 20); stale entries are garbage-collected automatically
- **IP allow list** — optional CIDR/IP whitelist in `auth.allowed_ips`; empty = allow all
- **Error sanitization** — internal errors are logged server-side but never exposed in HTTP responses
- **Security headers** — `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Cache-Control: no-store`, `Content-Security-Policy: default-src 'none'`
- **Minimal health endpoint** — unauthenticated `/v1/health` returns only `{"status":"ok"}` with no version or database details
- **Connection limits** — `IdleTimeout` (120s), `MaxHeaderBytes` (8KB), `RequestTimeout` (30s per-request context deadline)
- **Auth failure delay** — 500ms sleep on failed auth to slow brute-force attempts

## CLI

```
mdbapi run          Run in foreground (dev/debug)
mdbapi install      Install Windows service
mdbapi uninstall    Remove Windows service
mdbapi start        Start service
mdbapi stop         Stop service
mdbapi check        Validate config + test DB connections
mdbapi keygen       Generate API key (-add-to-config to append it)
mdbapi fixacl       Lock down config directory ACLs
mdbapi version      Print version
```

## Tailscale tunnel

Install with `-Tsnet` to embed a Tailscale node directly in the service. The API becomes accessible on your tailnet without opening ports or configuring firewalls.

```yaml
tunnel:
  provider: tsnet
  tsnet:
    hostname: mdbapi                              # appears as mdbapi.<tailnet>.ts.net
    auth_key: "${TS_AUTHKEY}"                      # pre-auth key from https://login.tailscale.com/admin/settings/keys
    state_dir: 'C:\ProgramData\MDBService\tsnet'   # persistent node state
    funnel: false                                  # true = expose publicly via Tailscale Funnel (port 443)
```

Generate a [reusable auth key](https://login.tailscale.com/admin/settings/keys) (check "Reusable" and "Ephemeral" if you don't want stale nodes). Set it as an environment variable for the service:

```powershell
$regPath = "HKLM:\SYSTEM\CurrentControlSet\Services\MDBRestService"
Set-ItemProperty -Path $regPath -Name Environment -Value @("TS_AUTHKEY=tskey-auth-...")
Restart-Service MDBRestService
```

Once running, the API is reachable at `http://mdbapi.<tailnet>.ts.net:8080/v1/health` from any device on your tailnet.

## Logs

When running as a service, logs go to `C:\ProgramData\MDBService\mdbapi.log` (JSON format). Rotation is configurable:

```yaml
service:
  log_file: 'C:\ProgramData\MDBService\mdbapi.log'
  log_max_size: 50       # MB — rotate when file exceeds this size
  log_max_files: 5       # number of rotated files to keep
  log_max_age: 30        # days — delete rotated files older than this
  log_compress: true     # gzip rotated log files
```

```powershell
# Tail the log
Get-Content C:\ProgramData\MDBService\mdbapi.log -Tail 50 -Wait
```

When running in foreground (`mdbapi run`), logs go to stderr.

## Auto-update

When enabled, checks GitHub Releases hourly. Downloads, verifies SHA256, swaps binary, restarts via Windows SCM recovery.

```yaml
updater:
  enabled: true
  github_token: ""          # optional, avoids rate limits
```

## Building from source

```bash
go build -o mdbapi.exe ./cmd/mdbapi                          # standard
go build -tags tsnet -o mdbapi_tsnet.exe ./cmd/mdbapi         # with tailscale
```

## License

MIT
