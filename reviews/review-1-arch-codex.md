## Summary Verdict

The vision document is clear, well-scoped, and picks a reasonable stack for the problem. The *engineering* decisions are mostly sound. However, the security model has two serious gaps that are currently papered over with open questions — the SQL passthrough endpoint and the API key lifecycle — and the deployment story leaves the hardest operational decisions (service account, secret injection, TLS surface) entirely to the user. For a tool that will sit next to decades-old business data on machines run by IT generalists, "we'll document it" is not a mitigation strategy. These need concrete answers before Phase 1 code lands.

---

## Strengths

- **Scope discipline.** Four endpoints, single binary, no database, no UI. This is the right call. Scope creep is the #1 killer of "bridge" tools.
- **Stack choices are solid.** `alexbrainman/odbc` via syscall avoiding CGo is the correct call for a self-contained Windows binary. Go 1.22 stdlib routing removes a dependency without sacrificing routing power. `kardianos/service` is the de-facto standard here.
- **`127.0.0.1` default listen.** Binding loopback-only by default is the right default posture. A lot of tools get this wrong.
- **Env var substitution for the API key.** `${MDBAPI_KEY}` is the right shape — better than a plaintext literal default.
- **`readonly: true` default in config examples.** Good instinct. Document this more loudly — it belongs in a bold warning, not just an example.
- **Open question acknowledgment.** The document is honest about what isn't decided. That's better than false confidence.

---

## Critical Issues

### 1. SQL Passthrough Is an Arbitrary Code Execution Surface — Decide Now, Not in Phase 1

**What's wrong:** `POST /{db}/query` accepts `{ "sql": "...", "params": [...] }`. This is not a query API — it's a database console. Any caller with the API key can run `DROP TABLE`, `UPDATE`, `DELETE`, or read tables they weren't meant to access. The open question ("whitelist SELECT-only, or allow arbitrary SQL?") treats this as a preference. It isn't — it's the primary attack surface of the entire tool.

The parameterized `params` field is good for preventing injection *within* a parameterized query, but it does nothing to limit what SQL the caller constructs. `SELECT * FROM Customers` with no params is perfectly valid and leaks everything. So is `DELETE FROM Orders`.

**Why it matters:** The target users are IT staff at small businesses. The MDB contains payroll, inventory, customer records. If someone steals or brute-forces the API key, `readonly: any` means full database write access from anywhere the key is valid.

**What to do instead:**
- **Default to `readonly` mode.** The passthrough endpoint should only execute statements that begin with `SELECT` after stripping whitespace and comments. Reject anything else at the middleware layer.
- **Add per-database `sql_passthrough` config** (`readonly | any | disabled`) with `readonly` as the default and a loud warning logged on startup if `any` is set.
- **Add a row limit cap.** Even in readonly mode, `SELECT * FROM LargeTable` with no limit is a denial-of-service. Enforce a max-rows ceiling (configurable, default 1000) server-side regardless of what the client sends.
- Decide and document this **before Phase 1**. The middleware structure needs to know.

---

### 2. The API Key Has No Lifecycle and No Brute-Force Protection

**What's wrong:** A single static API key for all databases, no rate limiting, no lockout, no rotation mechanism. The key is validated in a bearer token header over plain HTTP (when `tunnel: none` and the user brings their own proxy — more on that below). There is no mention of:
- What happens when the key needs to be rotated (answer: edit config, restart service)
- Whether failed auth attempts are logged (not stated)
- Any rate limiting on auth failures
- Per-database key scoping (the key grants access to all aliases)

A bearer token checked against a static string is susceptible to brute-force if the service is reachable from a network.

**Why it matters:** IT staff will share this key in Slack, commit it to scripts, use it in Excel macros. It will leak. When it does, there's no rotation path that doesn't require a service restart, and no audit log of what was accessed.

**What to do instead:**
- **Require minimum key entropy** (reject keys shorter than 32 hex chars at startup via `mdbapi check`).
- **Log all auth failures** with source IP and timestamp. Structured log, not just stderr.
- **Add a configurable `auth_failure_delay`** (e.g., 500ms sleep on 401) to make brute-force costly.
- **Consider multiple keys** (`auth.keys: [key1, key2]`) so rotation doesn't require a cutover window — add new key, deploy, remove old key.
- Per-database key scoping is Phase 2 territory, but the config shape should accommodate it from day one.

---

### 3. Service Account Is Unspecified — Default Will Be SYSTEM

**What's wrong:** `kardianos/service` installs a Windows service. When `sc create` is called without a `-obj` parameter, the service runs as `LocalSystem` (SYSTEM). SYSTEM has read/write access to essentially the entire machine: all files, registry, network shares. The vision document says nothing about what account to use.

**Why it matters:** SYSTEM is the wrong account for a network-facing API server. If the Go HTTP handler has a bug that allows path traversal or command injection (unlikely but not impossible), the blast radius is the entire machine. More practically: SYSTEM's ODBC sessions may have different driver visibility than the installing user, causing silent failures on some machines.

**What to do instead:**
- `mdbapi install` should create and use `NT SERVICE\MDBRestService` (the managed service account pattern) or accept a `--user` flag.
- Document the minimum permissions required:
  - **Read** access to each `databases[].path` directory
  - **Read/Write** access to the log directory (`C:\ProgramData\MDBService\`)
  - **No** network logon, no interactive logon, no admin rights
- The ODBC Access Database Engine has COM registration requirements — test that `NT SERVICE\...` accounts can load `aceodbc.dll` and document the known failure mode if they can't.

---

### 4. Config File ACLs Are Not Addressed — ProgramData Is World-Readable by Default

**What's wrong:** The config lives at `C:\ProgramData\MDBService\`. On a default Windows install, `C:\ProgramData` is readable by all authenticated users (DACL: `Users: Read & Execute`). The config file will contain database file paths (business-critical information) and potentially the literal API key if env var substitution isn't configured or silently fails.

**Why it matters:** Any local user can read the config, learn the API key, and start making API calls. On shared business machines (which is exactly the target environment), this is a real threat.

**What to do instead:**
- `mdbapi install` should **set restrictive ACLs** on the config directory at install time: `SYSTEM: Full Control`, `Administrators: Full Control`, `Users: (none)`. This is two lines of PowerShell or a `golang.org/x/sys/windows` call.
- **Fail loudly if env var substitution fails.** If `${MDBAPI_KEY}` is in the config but `MDBAPI_KEY` is not set in the environment, the service should refuse to start with a clear error — not start with the literal string `${MDBAPI_KEY}` as the key (which some YAML loaders will happily pass through).
- Document how to set the env var for a Windows service (`sc config ... obj= password=` or registry `HKLM\SYSTEM\CurrentControlSet\Services\<name>\Environment`). This is non-obvious and most IT staff will get it wrong.

---

## Suggestions

### 5. `tunnel: none` + No TLS Config = Silent HTTP Exposure Gap

The document says the default listen is `127.0.0.1:8080` and `tunnel: none`. This is safe when the user keeps the loopback default. But there's no TLS configuration in the YAML schema, and the binary has no TLS termination.

The actual exposure path is: user wants LAN access → changes `listen: 0.0.0.0:8080` → traffic is now plaintext HTTP on the local network → API key in every request header → anyone on the same switch can sniff it.

The `tunnel: none` recommendation to use Cloudflare Tunnel is a good one, but Cloudflare Tunnel proxies to localhost — it doesn't help if the user wants LAN access *without* internet exposure.

**Suggestion:** Add a `server.tls_cert` / `server.tls_key` config block (even if Phase 5). Document three deployment patterns explicitly:
1. **Localhost only** (default, `127.0.0.0:8080`) — no TLS needed, same-machine tools only
2. **LAN access** — bind to `0.0.0.0:8080` + TLS cert (self-signed is fine for LAN) — explain how
3. **Internet access** — Cloudflare Tunnel to localhost (recommended) or tsnet

Without this, users will cargo-cult `0.0.0.0:8080` from Stack Overflow and expose plaintext HTTP.

---

### 6. Auto-Update Trust Model Is Unaddressed

`go-selfupdate` downloads and replaces the running binary from GitHub Releases. The document mentions `checksums.txt` in the release assets, but doesn't say whether the updater:
- Verifies checksums before replacing the binary
- Verifies the GitHub Release is signed (it isn't, GitHub doesn't sign releases)
- Falls back gracefully if the download is corrupted or the signature check fails

A compromised GitHub account or a MITM on the download could deliver a malicious binary that then runs as a Windows service (possibly as SYSTEM). This is supply-chain risk.

**Suggestion:** Verify the SHA256 checksum from `checksums.txt` before applying any update. Use HTTPS (already the case with GitHub). Consider embedding a public key and signing releases with `cosign` — `go-selfupdate` supports this. At minimum, log what binary hash was running before and after each update.

---

### 7. The `GET /{db}/{table}` Filter Params Need Explicit Parameterization Commitment

The document says `GET /{db}/{table}` supports `?field=val` filter params. This is the classic SQL injection entry point for query-builder style APIs. "Filter params" need to be built as parameterized queries against a whitelist of valid column names (validated against the schema, not just passed through). The document doesn't say this explicitly.

**Suggestion:** In the `mdb/` package design, document that column names are validated against `information_schema` (or the ODBC schema API) and values are always passed as `?` parameters — never interpolated into the SQL string.

---

## Questions for the Author

1. **Who installs this?** An IT generalist following a README, or a developer? The answer changes how much hand-holding `mdbapi install` needs to do (ACLs, service account creation, firewall rules).

2. **Is write access via the passthrough endpoint a real use case, or is this purely a read bridge?** If it's read-only in practice, make it read-only in code and remove the ambiguity entirely. The `readonly: true` on database aliases and the open question on passthrough SQL suggest writes are at least considered.

3. **What's the expected key management story?** Is the API key meant to be rotated? Who holds it — a developer calling the API from another system, or a shared secret embedded in an Excel macro? The answer drives whether multi-key support matters.

4. **Is LAN access (not internet, not localhost) a real scenario?** A business might want `mdbapi` accessible from other machines on the same network without going through the internet. This is the gap where TLS-in-binary matters most — Cloudflare Tunnel doesn't solve it.

5. **How does the service get its env var?** You've specified `${MDBAPI_KEY}` substitution, but Windows services don't inherit the installing user's environment. The mechanism for injecting `MDBAPI_KEY` into the service's environment isn't documented. Is this `sc config`, the registry, a `.env` file the config loader reads? This needs to be answered before `mdbapi install` is implemented.
