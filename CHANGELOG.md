# Changelog

All notable changes to mdbapi will be documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
This project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Initial release: self-contained Windows service for read-only REST access to MDB/ACCDB files
- Four HTTP endpoints: list databases, list tables, query table, SQL passthrough (SELECT only)
- Multi-key authentication (`auth.keys`), 32-char minimum, 500 ms failure delay
- `mdbapi keygen [-add-to-config]` — generate cryptographically random API keys
- `mdbapi install` — installs Windows service as `NT SERVICE\<name>` virtual account
- `mdbapi fixacl` — restricts config directory ACLs to SYSTEM + Administrators
- `mdbapi check` — validates config, probes ODBC driver, tests all DB connections
- Auto-update via GitHub Releases with SHA256 checksum verification
- Optional Tailscale tsnet tunnel via `-tags tsnet` build
- Optional TLS via `server.tls_cert` / `server.tls_key`
- Rotating log file via `service.log_file` (lumberjack, 50 MB / 5 backups)
- SCM recovery actions: restart after 5 s on any failure (including clean `os.Exit(1)`)
- Access type normalization: Yes/No → bool, Date → RFC3339, Binary → base64, Hyperlink → plain text
- `SELECT TOP N` pagination (Access SQL dialect); offset emulated by client-side skip
- Column whitelist filter parameters validated against live schema
- `mdbapi run` — foreground mode for development

[Unreleased]: https://github.com/marcfargas/mdbapi/compare/HEAD...HEAD
