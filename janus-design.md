# Company Integration Platform — Design Document

## Purpose

This document describes the architecture for a **private monorepo** that centralizes all company-specific integration logic across multiple external systems (Odoo, TAAF, MDB, and future backends). The goal is to eliminate duplicated business logic across apps and provide a single, well-tested foundation that can be consumed by client applications, server-side services, MCP servers, and AI agents.

This repo is **separate from** the public [`odoo-toolbox`](https://github.com/marcfargas/odoo-toolbox), which remains an independent open-source project published to npm. This repo depends on `odoo-toolbox` packages as external npm dependencies.

---

## Problem

We interact with several external systems — Odoo (ERP), TAAF (real estate registry), MDB (internal data), and others over time. Each has its own API, authentication patterns, and data shapes. On top of these raw APIs, we layer company-specific conventions: which fields we use, how records map across systems, default values, validation rules, and multi-system workflows.

Today this logic is reimplemented in each application that needs it. This causes:

- **Drift** between apps that should behave identically.
- **Wasted effort** re-encoding the same business rules.
- **AI agents** that lack access to company-specific semantics and must be re-taught per project.
- **No single place** to test cross-system behavior.

We also anticipate needing the same logic in multiple languages (TypeScript today, Python eventually), and some integrations will require server-side execution (secrets, atomicity, server-to-server APIs).

---

## Architecture Overview

```
company-platform/
│
├── packages/
│   ├── js/
│   │   ├── core/               # Shared types, auth, errors, config
│   │   ├── odoo/               # Company Odoo client (wraps @marcfargas/odoo-client)
│   │   ├── taaf/               # TAAF real estate client
│   │   └── mdb/                # MDB endpoint client
│   │
│   └── python/
│       ├── core/               # Shared types, auth, errors
│       ├── odoo/               # Company Odoo client
│       └── taaf/               # TAAF client
│
├── services/                   # Server-side endpoints
│   ├── taaf-sync/              # Example: serverless function for TAAF
│   └── odoo-webhook/           # Example: webhook handler
│
├── mcp/                        # Private MCP server(s)
│   └── company-mcp/            # Extends @marcfargas/odoo-mcp, adds TAAF/MDB
│
├── skills/                     # Private AI skills / knowledge modules
│   ├── odoo/                   # Company-specific Odoo conventions
│   ├── taaf/                   # TAAF domain knowledge
│   └── workflows/              # Cross-system workflow descriptions
│
├── test-fixtures/              # Shared test cases across languages
│   ├── merge-partner/
│   │   ├── case-basic.json
│   │   ├── case-conflicting-emails.json
│   │   └── case-taaf-missing.json
│   └── create-property-listing/
│       └── ...
│
├── package.json                # npm workspaces root
├── pyproject.toml              # Python workspace config (if using uv/hatch)
├── justfile                    # Top-level task runner
├── .claude/                    # Claude Code project config
│   ├── settings.json
│   └── AGENTS.md
└── README.md
```

---

## Layer Responsibilities

### `packages/` — Language-specific clients

Each system gets a package per language. These are **pure library code** with no runtime opinions — no `process.env` reads, no assumption about browser vs Node vs serverless. Credentials and config are passed in as parameters.

**`packages/js/core/`** provides:

- Shared TypeScript types used across all system clients (e.g., `CompanyPartner`, `Address`).
- Common auth patterns, error types, retry/backoff utilities.
- Config loading interface (not implementation — consumers provide config).

**`packages/js/odoo/`** provides:

- Imports `@marcfargas/odoo-client` from npm as a dependency.
- Wraps it with company-specific knowledge: which fields to read on `res.partner`, default domain filters, property field mappings, naming conventions.
- Higher-level operations like `getPartnerWithProperties()` that encode our Odoo conventions.

**`packages/js/taaf/`** and **`packages/js/mdb/`** follow the same pattern for their respective APIs.

**`packages/python/`** mirrors the JS structure. Each Python package is a standalone `pyproject.toml`-based project. The Odoo package wraps whichever base Python Odoo library we choose (e.g., `odoorpc` or raw `xmlrpc`).

### `services/` — Server-side endpoints

For integrations that cannot run client-side — secrets that can't be exposed, multi-step atomic operations, server-to-server-only APIs. Each service is a separate deployable unit (serverless function, container, etc.).

Services **import from `packages/`** — they are thin HTTP/event wrappers around existing library code. The business logic lives in the packages, not in the services.

A service might be a single cloud function or a proper REST API depending on the use case. Different services can use different deployment strategies. They coexist in `services/` as independent projects.

### `mcp/` — AI tool layer

A private MCP server that extends the public `@marcfargas/odoo-mcp` and adds:

- Company-specific Odoo tools (using `packages/js/odoo/`).
- TAAF and MDB tools.
- Cross-system operations (e.g., "find partner across Odoo and TAAF").

The MCP server may call packages directly (for in-process operations) or call services (for operations requiring the server-side path). From the AI's perspective, these are all just tools.

### `skills/` — AI knowledge modules

Private progressive skill modules following the same pattern as `odoo-toolbox/skills/`. These teach AI agents about company-specific conventions, field mappings, and workflows. They reference the packages and MCP tools but are pure documentation.

### `test-fixtures/` — Cross-language contract testing

Shared JSON fixtures that define inputs, expected outputs, and edge cases for business logic functions. Both TypeScript and Python test suites load the same fixtures.

This is the mechanism that keeps multi-language implementations in sync. The workflow:

1. Define a new business rule as a fixture (input → expected output).
2. Make the TypeScript implementation pass.
3. When writing the Python implementation, run the same fixtures.
4. If both pass the same cases, they behave identically.

Fixture structure:

```json
{
  "description": "Basic partner merge — Odoo has email, TAAF has phone",
  "input": {
    "odoo_partner": {
      "id": 42,
      "name": "Acme Corp",
      "email": "info@acme.com",
      "phone": false
    },
    "taaf_record": {
      "ref": "TAAF-1234",
      "contact_phone": "+34 600 000 000"
    }
  },
  "expected": {
    "name": "Acme Corp",
    "email": "info@acme.com",
    "phone": "+34 600 000 000",
    "sources": ["odoo", "taaf"]
  }
}
```

---

## Dependency Flow

```
apps / scripts / notebooks
        │
        ▼
   ┌─────────┐     ┌──────────┐
   │ services │     │   mcp    │
   └────┬─────┘     └────┬─────┘
        │                │
        ▼                ▼
   ┌─────────────────────────┐
   │       packages/js/*      │  (or packages/python/*)
   │  ┌──────┬───────┬──────┐ │
   │  │ odoo │ taaf  │ mdb  │ │
   │  └──┬───┴───┬───┴──┬───┘ │
   │     └───────┼──────┘     │
   │          core/           │
   └──────────┬───────────────┘
              │
              ▼
   ┌────────────────────────┐
   │ External: npm packages  │
   │ @marcfargas/odoo-client │
   │ @marcfargas/odoo-mcp    │
   └────────────────────────┘
```

Dependencies only flow downward. Each layer imports from the layer below, never upward or sideways.

---

## Key Design Decisions

### Public toolbox stays separate

`odoo-toolbox` is a public open-source project with its own release cycle, CI, and npm publishing. This repo depends on it via npm, the same way any external consumer would. No git submodules, no symlinks, no special treatment.

When `odoo-toolbox` publishes a new version, this repo updates like any other dependency.

### Packages are runtime-agnostic

No package reads from `process.env`, `window`, or any environment directly. All configuration (URLs, credentials, database names) is injected by the consumer. This means the same package works when imported by:

- A React/Vue app (browser)
- A CLI tool (Node)
- A serverless function (Lambda/Cloud Functions)
- An MCP server (Node)
- A test suite (vitest/pytest)

The service layer and MCP layer are where environment-specific wiring lives.

### TypeScript is the primary language

TypeScript is the first-class implementation language. It is the source of truth for business logic. Python implementations come second and are validated against the same test fixtures. When a new business rule is added, it is implemented in TypeScript first.

### No shared schema layer (for now)

We considered a formal schema definition layer (JSON Schema, TypeSpec, Protobuf) to generate types across languages. We are deferring this until we have two active language implementations and can evaluate whether the indirection is worth it. Until then, each language package owns its types and the shared test fixtures are the synchronization mechanism.

### Adding a new system

When a new external system needs integration:

1. Create `packages/js/<system>/` with the client library.
2. Add test fixtures in `test-fixtures/<relevant-operations>/`.
3. Add skills in `skills/<system>/` for AI agent knowledge.
4. Add MCP tools in `mcp/company-mcp/` if AI access is needed.
5. If server-side execution is needed, add a service in `services/`.

---

## Tooling

### JavaScript / TypeScript

- **npm workspaces** for package management (consistent with `odoo-toolbox`).
- **vitest** for testing.
- **changesets** for versioning (internal, not published to public npm).
- **eslint + prettier** for linting/formatting.
- **tsconfig** project references for build ordering.

### Python

- **uv** or **hatch** for project management, one `pyproject.toml` per package.
- **pytest** for testing, with a shared conftest that loads JSON fixtures.
- **ruff** for linting/formatting.

### Cross-language

- **`justfile`** at the root as the top-level task runner. Delegates to each language's native tooling. Example commands:
  - `just test` — runs all tests (JS + Python).
  - `just test-js` — runs JS tests only.
  - `just test-python` — runs Python tests only.
  - `just build` — builds all packages.
  - `just lint` — lints everything.
- **CI** runs both language test suites. A fixture change triggers both.

---

## Migration Path

### Phase 1 — JS foundation

Set up the monorepo with `packages/js/core/` and `packages/js/odoo/`. Extract existing company-specific Odoo logic from current apps into the package. Write test fixtures for existing business rules. Migrate one app to consume the package instead of its internal implementation.

### Phase 2 — Additional systems

Add `packages/js/taaf/` and `packages/js/mdb/`. Add the MCP server and skills.

### Phase 3 — Services

When a specific integration requires server-side execution, add it as a service in `services/`. The service imports from `packages/js/*`.

### Phase 4 — Python

When a Python consumer materializes, add `packages/python/*` implementations validated against the existing test fixtures.

---

## Naming

The repo should be named for what it is — the company's canonical integration layer — not for any single backend. Suggested names:

- `company-platform`
- `company-integrations`
- `platform`

Avoid names like `odoo-private` or `taaf-client` that anchor to a single system.
