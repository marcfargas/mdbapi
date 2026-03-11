# Review Synthesis: mdbapi Vision Document

**Note:** Task specification indicated 4 reviews (2 waves), but only 2 reviews were provided in the input. This synthesis covers the available reviews: architecture review (Codex) and implementation review (Claude).

---

## Executive Summary

Both reviewers agree the vision is **buildable and well-scoped**, but both flag it as **not ready for implementation** without resolving critical security and operational gaps first. The architecture reviewer sees this as a security hardening problem; the implementation reviewer sees it as an underestimated complexity problem. Both are correct.

**Recommendation:** Do not start Phase 1 code until the 5 critical issues below are decided and documented.

---

## Unanimous Verdicts

Both reviewers converge on these points:

1. **Scope discipline is excellent.** Four endpoints, single binary, no database, no UI — exactly right for a bridge tool.

2. **Stack choices are sound.** `alexbrainman/odbc` (syscall, no CGo), Go 1.22 stdlib routing, and `kardianos/service` are all appropriate for this problem space.

3. **Loopback-only default (`127.0.0.1:8080`) is the right security posture.** Good instinct that many tools get wrong.

4. **Readonly should be the strong default everywhere.** Both reviews emphasize this for SQL passthrough, database aliases, and overall system posture.

5. **Timeline is optimistic.** The 6–9 day estimate doesn't account for Windows service hardening, ODBC quirk handling, and cross-architecture validation. Realistic: **2–3 weeks**.

6. **The first production failures will not come from HTTP routing.** They'll come from (a) Access driver architecture mismatches on customer machines, (b) Access metadata/type quirks, and (c) Windows service update behavior.

---

## Key Divergences

Where reviewers emphasize different concerns:

### Scope Strategy
- **Architecture reviewer:** Demands security decisions be made **before Phase 1** — especially SQL passthrough policy, API key lifecycle, and service account model. "Document it later" is not acceptable for a tool handling business-critical data.
- **Implementation reviewer:** Suggests **cutting features for MVP** — defer ARM64, defer auto-apply updates, defer tunnel providers beyond `none`. Prove the ODBC layer works first.

**Resolution needed:** These aren't contradictory — security *decisions* must happen upfront (arch), but *features* can be deferred (impl). The vision needs to distinguish between "decided but not implemented" and "TBD."

### What Will Break First
- **Arch reviewer:** API key leakage, config file ACL exposure, SQL passthrough abuse, SYSTEM account blast radius.
- **Impl reviewer:** Driver architecture mismatch (32-bit Office on 64-bit app), `MSysObjects` permission errors, file-locked update failures, date/boolean serialization inconsistencies.

**Implication:** Both attack surfaces are real. Testing strategy needs both security threat modeling *and* compatibility matrix validation.

---

## Critical Issues (Must Address Before Phase 1)

### 1. SQL Passthrough Endpoint Is Arbitrary Code Execution — Decide the Policy Now

**Problem:** `POST /{db}/query` accepts `{ "sql": "...", "params": [...] }`. With the API key, any caller can run `DROP TABLE`, `DELETE`, or read unrestricted data. The vision treats this as an open question; the arch reviewer correctly identifies it as the primary attack surface.

**Decision required:**
- Default to **readonly mode** (only `SELECT` statements allowed, enforced at middleware layer).
- Add per-database `sql_passthrough: readonly | any | disabled` config.
- Enforce server-side row limit (default 1000, configurable) even in readonly mode to prevent DoS.
- Log a loud warning on startup if any database has `sql_passthrough: any`.

**Timeline impact:** Must be decided before Phase 1 API middleware is written.

---

### 2. API Key Has No Lifecycle, No Brute-Force Protection, No Audit Trail

**Problem:** Single static key for all databases, no rotation mechanism without service restart, no rate limiting, no failed auth logging. When the key leaks (and it will — IT staff will commit it to scripts, share it in Slack, embed it in Excel macros), there's no containment.

**Decision required:**
- Require **minimum 32-char hex entropy**, reject weak keys at startup (`mdbapi check`).
- **Log all auth failures** with source IP and timestamp (structured log).
- Add configurable **auth failure delay** (500ms sleep on 401) to slow brute-force.
- Support **multiple keys** (`auth.keys: [key1, key2]`) for zero-downtime rotation.
- Document that this is the config shape now; per-database key scoping comes later.

**Timeline impact:** Auth middleware design must accommodate multi-key from day one.

---

### 3. Windows Service Account Defaults to SYSTEM — Unacceptable Blast Radius

**Problem:** `kardianos/service` without `-obj` flag runs as `LocalSystem` (full machine access). Vision says nothing about service account strategy. If a handler bug allows path traversal or command injection, the entire machine is compromised. More practically: SYSTEM's ODBC driver visibility differs from the installing user's, causing silent failures.

**Decision required:**
- `mdbapi install` creates and uses **`NT SERVICE\MDBRestService`** (managed service account), or accepts `--user` flag.
- Document **minimum permissions**: read access to each `databases[].path`, read/write to `C:\ProgramData\MDBService\`, no admin rights.
- Test that `NT SERVICE\...` accounts can load ACE ODBC driver (`aceodbc.dll`) and document failure modes.

**Timeline impact:** Affects `mdbapi install` implementation (Phase 3).

---

### 4. Access ODBC Quirks Are Underestimated — Need Fallback Strategy and Type Contract

**Problem:** The impl reviewer identifies three fragile areas not addressed in the vision:
- `MSysObjects` table listing often fails with permission errors.
- Date literals and Yes/No fields have inconsistent representations across drivers.
- Driver architecture mismatches (32-bit Office, 64-bit app, ARM64 emulation) will be the #1 support issue.

**Decision required:**
- **Table discovery:** Do not rely solely on `MSysObjects`; implement fallback via ODBC schema API (`SQLTables`). Document expected error messages.
- **Type normalization:** Define explicit JSON contract — dates as RFC3339, booleans as `true/false` (normalize `-1/0` and `1/0` internally), OLE/binary fields as base64 or null.
- **Driver diagnostics:** Add startup check that detects installed driver architecture and fails fast with actionable guidance on mismatch.

**Timeline impact:** Must be part of Phase 1 `mdb/` package design. Add to test matrix.

---

### 5. Config File ACLs and Env Var Injection Are Undocumented — Local Privilege Escalation Risk

**Problem:** Config at `C:\ProgramData\MDBService\` is world-readable by default on Windows (all authenticated users). If env var substitution fails silently, the literal `${MDBAPI_KEY}` string becomes the key. No documentation on how to inject `MDBAPI_KEY` into a Windows service environment (services don't inherit user environment).

**Decision required:**
- `mdbapi install` sets **restrictive ACLs** on config directory: `SYSTEM: Full Control`, `Administrators: Full Control`, `Users: (none)`. Two lines of PowerShell or `golang.org/x/sys/windows`.
- **Fail loudly** if env var substitution fails — refuse to start if `${MDBAPI_KEY}` is in config but `MDBAPI_KEY` not set.
- **Document the Windows service env var mechanism**: registry `HKLM\SYSTEM\CurrentControlSet\Services\<name>\Environment` or `sc config`.

**Timeline impact:** Affects `mdbapi install` and config loader (Phase 1 and 3).

---

## Tunnel Decision: Recommendation

**The open question:** Support Cloudflare Tunnel, tsnet, or just document external proxy setup?

**Synthesis:**
- **Arch reviewer** flags a gap: `tunnel: none` + no TLS config + LAN access = plaintext HTTP with API keys in headers. Cloudflare Tunnel doesn't help if users want LAN-only access (no internet). Suggests documenting three patterns: localhost-only (default), LAN + TLS, internet via Cloudflare Tunnel.
- **Impl reviewer** suggests deferring tunnel providers beyond `none` for MVP.

**Recommendation:**
1. **Phase 1 (MVP):** `tunnel: none` only. Default listen `127.0.0.1:8080`. Document localhost-only use case.
2. **Phase 2 or 3:** Add `server.tls_cert` / `server.tls_key` config for LAN access with self-signed certs. Document the LAN pattern explicitly.
3. **Phase 4 or 5:** Add Cloudflare Tunnel or tsnet integration for internet exposure.

**Rationale:** The LAN + TLS case is a real deployment scenario (small business wants API accessible from other machines without internet), but it doesn't block MVP. Localhost-only is sufficient for initial validation. The vision should document all three patterns *now* to guide the config schema, but implement them incrementally.

**Config shape to reserve now:**
```yaml
server:
  listen: "127.0.0.1:8080"
  tls_cert: ""  # future
  tls_key: ""   # future
tunnel:
  provider: none  # future: cloudflare | tsnet
  config: {}
```

---

## Risks the Vision Missed

Both reviewers identified critical gaps not covered in the original vision:

1. **Service account defaults** (arch) — No specification leads to dangerous SYSTEM default.
2. **Config file ACL exposure** (arch) — World-readable ProgramData exposes API keys and DB paths.
3. **Auto-update trust model** (arch) — No checksum verification or supply-chain mitigation documented.
4. **Filter param injection** (arch) — `GET /{db}/{table}?field=val` needs explicit parameterization commitment.
5. **Windows service env var injection** (arch) — How does `${MDBAPI_KEY}` actually get set? Not documented.
6. **Driver architecture mismatch** (impl) — 32-bit Office + 64-bit app is a support nightmare.
7. **`MSysObjects` permission failures** (impl) — Common on real customer DBs; no fallback strategy.
8. **Windows update file locking** (impl) — Running exe is locked; in-place update will fail without two-phase flow.
9. **Access type quirks** (impl) — No JSON type contract for dates, booleans, nulls, OLE fields.
10. **ARM64 feasibility unvalidated** (impl) — ACE driver may not exist for native ARM64.

---

## Concrete Next Actions

Before writing any Phase 1 code:

### 1. Update Vision Document (1 day)
- **Decide and document** the 5 critical issues above (SQL passthrough policy, API key lifecycle, service account model, ODBC quirk strategy, config ACLs).
- Add explicit **JSON type contract** for Access types (dates as RFC3339, booleans as true/false, binary as base64).
- Add **three deployment patterns** (localhost, LAN+TLS, internet+tunnel) with config examples.
- Add **architecture support matrix** (windows/amd64 primary, windows/arm64 experimental pending ACE driver validation).
- Document **update state machine** (download → verify checksum → backup old → apply → restart → rollback on failure).

### 2. Validate ODBC Reality (2 days)
- Test `alexbrainman/odbc` against real `.mdb` and `.accdb` files with:
  - Table listing via `MSysObjects` *and* `SQLTables` fallback
  - Date field roundtrip (various formats)
  - Yes/No field mapping
  - 32-bit vs 64-bit driver mismatch behavior
  - `NT SERVICE\...` account vs SYSTEM vs user account
- Document failure modes and recommended ACE driver installation.

### 3. Security Baseline Config (half day)
- Write reference `config.yaml` with all security settings at safe defaults:
  - `readonly: true` on all database aliases
  - `sql_passthrough: readonly` with row limit 1000
  - `auth.keys: ["${MDBAPI_KEY}"]` with entropy requirement
  - Commented examples showing unsafe settings with **bold warnings**

### 4. Revise Phase Plan (half day)
- **Phase 0 (new):** ODBC driver validation matrix and type contract testing (from step 2 above).
- **Phase 1:** Core MDB access layer with fallback metadata strategy, not just happy path.
- **Phase 2:** HTTP API with auth middleware supporting multiple keys from day one.
- **Phase 3:** Windows service with `NT SERVICE\...` account and config ACL setup.
- **Phase 4:** Two-phase update flow (download+verify → apply on restart).
- **Phase 5:** Optional tunnel providers (cloudflare, tsnet) and TLS config.

Adjust timeline to **2–3 weeks** realistic estimate.

### 5. Create Test Matrix Document (half day)
- Windows versions: 10, 11, Server 2019/2022
- Architectures: amd64 on x64, amd64 on ARM64 (emulation), native arm64 (experimental)
- ACE driver variants: 32-bit, 64-bit, redistributable vs full Office install
- Test DBs: `.mdb` (Jet), `.accdb` (ACE), varying security settings
- Document which combinations are supported vs experimental vs unsupported.

---

## Phase Reordering

**Original vision** implied: API → Service → Update → Tunnel.

**Recommended** (based on impl review):
1. **Phase 0 (new):** ODBC driver validation and type contract testing.
2. **Phase 1:** MDB access layer with robust metadata fallback and type normalization.
3. **Phase 2:** HTTP API with multi-key auth middleware.
4. **Phase 3:** Windows service with correct account and ACLs.
5. **Phase 4:** Two-phase update mechanism with checksum verification.
6. **Phase 5:** Optional TLS config and tunnel providers.

**Rationale:** Prove the ODBC layer works *before* building HTTP on top of it. The impl reviewer is right — driver mismatches and Access quirks are more likely to block progress than routing decisions.

---

## Summary

**Is the vision ready for implementation?** No — not yet.

**Is it salvageable?** Absolutely. The core idea is sound, the scope is tight, and the stack choices are appropriate.

**What's needed:** Spend 2–3 days answering the 5 critical questions above and validating ODBC reality. Once those are documented, the vision becomes a solid foundation.

**Tone check:** Both reviewers are constructive. The arch reviewer is direct about security gaps because the target users (IT generalists at small businesses) won't catch them. The impl reviewer is pragmatic about what will actually break. Neither is saying "don't build this" — they're saying "make these decisions first, then build it right."
