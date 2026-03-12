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

## API

Auth via `Authorization: Bearer <key>` or `X-API-Key: <key>`. All responses: `{"data": ...}` or `{"error": {"code": "...", "message": "..."}}`.

| Endpoint | Auth | Description |
|---|---|---|
| `GET /v1/health` | No | Status, version, database health counts |
| `GET /v1/` | Yes | List databases with alias, path, status |
| `GET /v1/{db}/tables` | Yes | List tables in a database |
| `GET /v1/{db}/{table}` | Yes | Query rows. Params: `field=value`, `limit`, `offset`, `sort`, `sort_desc` |
| `POST /v1/{db}/query` | Yes | SQL passthrough. Body: `{"sql": "SELECT ..."}`. Only SELECT/WITH allowed (403 otherwise) |

### Example

```bash
curl -H "Authorization: Bearer $KEY" http://localhost:8080/v1/inventory/Products?limit=10
```

```json
{
  "data": {
    "database": "inventory",
    "table": "Products",
    "count": 10,
    "rows": [
      {"id": 1, "name": "Widget", "active": true, "created": "2024-06-15T12:00:00Z"}
    ]
  }
}
```

## Read-only by design

- SQL layer rejects anything that isn't `SELECT` or `WITH` → HTTP 403
- `mdbapi fixacl` sets NTFS read-only ACLs on your database files
- No ODBC write path exists in the code

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

Build with `-tags tsnet` (or install with `-Tsnet`) to embed a Tailscale node directly in the service. The API becomes accessible on your tailnet without opening ports or configuring firewalls.

```yaml
tunnel:
  provider: tsnet
  hostname: mdbapi          # appears as mdbapi.<tailnet>.ts.net
```

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
