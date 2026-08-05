# IMPLEMENTATION.md — Groundwork

Quick-reference for AI coding agents working on this repository.

## 1. Source of Truth

`docs/Groundwork/` is the **primary source of truth** for all product requirements, features, data
model, design, and screen behavior. This file is a complementary quick-reference for stack,
conventions, and commands only.

- **`docs/` is read-only.** Never create, edit, delete, or rename anything under `docs/Groundwork/`.
  When code and docs disagree, or a doc is wrong or incomplete, **report it** — doc changes are made
  by the `product-shaper` agent, never by an implementor.
- When this file and `docs/Groundwork/` conflict, **the docs win**. Fix this file, not the docs.

**`docs/` is a git submodule** → `ahmadkhidir/groundwork-docs` (private). The parent repo stores only
a commit pointer, so docs access is granted separately from code access.

- After cloning: `git submodule update --init docs`. A **permission error** there means you have code
  access but not docs access — request it; do not proceed as if the product has no docs.
- An **empty `docs/` is never "no specs"**. It is an uninitialized submodule. Stop and say so rather
  than inventing requirements to fill the gap.
- **Push order is not optional:** push the docs repo *before* pushing any parent commit that
  references the new docs commit, or everyone else's `submodule update` fails on a SHA that exists
  nowhere. `push.recurseSubmodules=check` is configured to enforce this.
- **Bump the pointer only when committing code built against those docs** — `git add docs` in the
  same commit as that code. Between those moments, `modified: docs (new commits)` in the parent is
  accurate information (docs have moved ahead of the code); leave it alone.
- Staging the pointer is a parent-repo index change, **not** a write to `docs/`. It is expected of
  you; the read-only rule governs document contents.

## 2. Tech Stack

| Layer | Technology | Version |
|---|---|---|
| Web framework | Next.js (App Router) | `16.3.0` |
| UI runtime | React | `19.2.8` |
| Styling | Tailwind CSS v4 (`@theme`) | `4.3.3` |
| Charts | Recharts (wrapped in first-party components) | `3.10.1` |
| Auth | Auth.js (`next-auth`) + `@auth/pg-adapter` | `5.0.0-beta.32` / `1.11.3` |
| Language (web) | TypeScript | `7.0.2` |
| Backend | Go (single service — the only backend) | `1.26.3` |
| HTTP router | `github.com/go-chi/chi/v5` | `5.3.1` |
| Postgres driver | `github.com/jackc/pgx/v5` | `5.10.0` |
| Migrations | `github.com/pressly/goose/v3` | `3.27.3` |
| Analytical engine | `github.com/duckdb/duckdb-go/v2` (official, cgo) | `2.10505.0` |
| Model provider | `github.com/anthropics/anthropic-sdk-go` | `1.61.0` |
| Object storage | `github.com/aws/aws-sdk-go-v2/service/s3` | `1.106.4` |
| Tests | Go `testing` · Vitest · Playwright | — / `4.1.10` / `1.62.1` |

**Deployment target:** Fly.io (compute) · Neon (Postgres) · Cloudflare R2 (object storage).
Single region, scale-to-zero. See `docs/Groundwork/architecture.md`.

**Local development:** docker-compose provides Postgres and MinIO (S3-compatible, stands in for R2).
The DuckDB engine runs as a local OS process — this is `architecture.md`'s "implementation #1" of the
Engine Provider seam, not a mock.

**cgo is mandatory.** `CGO_ENABLED=1` and a C compiler are required for every Go build and test run
(the DuckDB driver). Fully static binaries are not available. Accepted constraint — see `risk_log.md`
R-010.

## 3. Project Structure

```
groundwork/
├── web/                          # Next.js client
│   ├── src/app/                  # App Router routes (see route map below)
│   ├── src/components/           # Shared UI components
│   │   ├── ui/                   # Primitives: Button, Card, Table, Badge, StatusPill...
│   │   └── charts/               # First-party chart components (FEAT-017)
│   ├── src/lib/                  # API client, auth helpers, formatters, config
│   ├── src/types/                # TS types mirroring docs/Groundwork/schema.md
│   └── src/styles/globals.css    # Tailwind v4 @theme — ALL design tokens live here
├── server/                       # Go API service (the single backend)
│   ├── cmd/api/                  # main.go — service entrypoint
│   ├── internal/
│   │   ├── api/                  # HTTP handlers, middleware, streaming (SSE)
│   │   ├── orchestrator/         # Durable runs/steps DAG engine (Phase 3)
│   │   ├── engine/               # Engine Provider seam: acquire/release
│   │   ├── ingest/               # Upload, normalize, profile, infer relationships
│   │   ├── semantic/             # Semantic layer store
│   │   ├── audit/                # Combined audit + model-egress log
│   │   ├── domain/               # Entity types from schema.md
│   │   ├── store/                # postgres/ and r2/ persistence
│   │   └── config/               # Tunable thresholds (single source — see §5)
│   └── migrations/               # goose SQL migrations
├── docs/Groundwork/              # READ-ONLY source of truth
├── docker-compose.yml            # Postgres + MinIO for local dev
└── IMPLEMENTATION.md
```

**Naming conventions**
- Go: standard idiomatic Go. Packages lowercase single-word. Exported identifiers documented with a
  comment beginning with the identifier name. Errors wrapped with `fmt.Errorf("...: %w", err)`.
- TypeScript: `PascalCase` components, `camelCase` functions/variables, `kebab-case` route segments.
- Files: Go `snake_case.go`; React components `PascalCase.tsx`.
- Migrations: `NNNNN_short_description.sql`, goose `-- +goose Up` / `-- +goose Down`.

**Route map** (from `docs/Groundwork/screens.md` — 17 screens):

| Route | Screen |
|---|---|
| `/` | Landing Page |
| `/signup`, `/login` | Sign Up / Login |
| `/workspaces` | Workspace List |
| `/workspaces/:id` | Workspace Home |
| `/workspaces/:id/upload` | File Upload |
| `/workspaces/:id/ingest/progress` | Ingest Progress |
| `/workspaces/:id/ingest/confirm` | Ingest Confirmation |
| `/workspaces/:id/runs/:runId` | Run Viewer |
| `/workspaces/:id/reports/:reportId` | Report View (`?claim=` drawer, `?export=` modal) |
| `/workspaces/:id/settings` | Workspace Settings |
| `/workspaces/:id/audit-log` | Egress / Audit Log |
| `/workspaces/:id/semantic-layer` | Semantic Layer / Corrections |
| `/workspaces/:id/deleted` | Deletion Proof |
| `/notifications` | Notifications |

## 3a. Theme & Token Map

**All design tokens live in `web/src/styles/globals.css` inside Tailwind v4's `@theme` block.**
There is no second token file, no JS theme object, and no `tailwind.config.js` color list. Tokens are
defined once as CSS custom properties; Tailwind generates the utilities.

**Dark mode:** a `@custom-variant dark (&:where(.dark, .dark *))` plus the same custom properties
re-declared under `.dark`. Every color and shadow token from `branding.md` carries **both** its light
and dark value. Components never branch on theme — they reference the token and the variant resolves.

**Mapping from `branding.md` → Tailwind token names:**

| branding.md | CSS custom property | Example utility |
|---|---|---|
| `primary`, `primary-hover`, `primary-light` | `--color-primary`, `--color-primary-hover`, `--color-primary-light` | `bg-primary` |
| `surface`, `surface-secondary` | `--color-surface`, `--color-surface-secondary` | `bg-surface` |
| `text-primary`, `text-secondary` | `--color-text-primary`, `--color-text-secondary` | `text-text-primary` |
| `border` | `--color-border` | `border-border` |
| `success`, `warning`, `error`, `info` | `--color-success` … | `text-error` |
| `provenance`, `provenance-light`, `code-accent` | `--color-provenance` … | `text-provenance` |
| `chart-cat-1`…`8`, `chart-seq-1/3/5`, `chart-div-*` | `--color-chart-cat-1` … | passed to Recharts by name |
| `display`, `h1`…`caption`, `code` | `--text-display` … (+ paired line-height/weight) | `text-h1` |
| `space-xs`…`space-3xl` | `--spacing-xs` … | `p-md`, `gap-lg` |
| `radius-sm`…`radius-full` | `--radius-sm` … | `rounded-md` |
| `shadow-sm`…`shadow-lg` | `--shadow-sm` … | `shadow-md` |

> **Naming note:** branding's color token `text-primary` becomes the utility `text-text-primary`
> (Tailwind prefixes color utilities with `text-`). This is intentional — the branding token name is
> preserved rather than renamed to something that would no longer match the docs.

**Non-token values:** `button-height` (40px), `input-height` (40px), and `icon-sm/md/lg` are sizing
constants exposed as `--size-*` custom properties and consumed via arbitrary-value utilities where no
native Tailwind slot exists.

**Skipped tokens:** none yet. Any token that cannot be mapped during Foundation gets recorded here
with the reason. **A skipped token is never inlined as a raw value at a call site** — report it
instead.

**Fonts:** IBM Plex Sans (display/headings), Inter (body), IBM Plex Mono (SQL/code/data values),
loaded via `next/font` and bound to `--font-display`, `--font-body`, `--font-mono`.

> This mapping is verified against the `vanguard-stack-foundation` skill during the Foundation phase
> and updated here if that reference dictates otherwise.

## 4. Coding Style & Conventions

**Go**
- `gofmt` (enforced) and `golangci-lint` clean before any commit.
- Constructor injection over globals. No package-level mutable state.
- `context.Context` is the first parameter of every function doing I/O, and cancellation is honored —
  this is load-bearing for the interruptible pipeline, not decoration.
- All SQL uses parameterized queries via pgx. Never build SQL by string concatenation of user input.
- Every exported type, function, and interface carries a doc comment.
- The **Engine Provider seam** (`acquire(workspace_id) → EngineHandle` / `release(handle)`) is the
  only route to a DuckDB engine. Nothing outside `internal/engine` may know how an engine is
  provisioned.

**TypeScript / React**
- Server Components by default; `"use client"` only where interactivity or streaming demands it.
- SSR/SSG is limited to marketing, auth, and the static shell. Workspace, run viewer, and report
  surfaces are client-side and stream from the Go API. (`architecture.md` boundary rule.)
- No `any`. Types mirroring `schema.md` live in `src/types/` and are the single definition.
- Styling exclusively through theme tokens (§3a). **No raw hex, no magic pixel values.**
- Data fetching goes through `src/lib/api-client.ts` — never a bare `fetch` in a component.

**Testing**
- Go: table-driven tests alongside the code (`*_test.go`).
- Web: Vitest for units/components, Playwright for flows from `user_flow.md`.
- Every acceptance criterion in `features.md` becomes at least one test case.
- Every screen state in `screens/` (loading, empty, error, offline, submitting, validation error) gets
  a rendering test.

## 5. Behavior Rules

1. **Never write to `docs/`.** Report doc defects; do not fix them.
2. **Never commit secrets.** Config comes from environment variables; `.env` is git-ignored and
   `.env.example` documents every key.
3. **Never hardcode a design token value.** Style through the theme (§3a) or report the gap.
4. **Never invent a requirement.** Anything not in the docs or confirmed by the user is surfaced as a
   question or a stated assumption.
5. **The Next.js layer must never** perform run orchestration, engine access, semantic-layer mutation,
   or any DuckDB access. Rendering, routing, auth-session handling, and BFF proxying only.
   (`architecture.md` boundary rule.)
6. **The model never answers from data in context.** It plans and writes SQL; the engine returns
   facts; the model narrates over those facts. There is no bypass code path — this is foundational.
7. **Every payload sent to the model is written to the audit/egress log in the same code path as the
   outbound call** — never reconciled after the fact.
8. **Every table keyed to a workspace carries `workspace_id`.** The deletion cascade's correctness
   depends on this discipline everywhere, not on the cascade code alone.
9. **Verification and privacy features are never tier-gated.** Tiers gate usage volume, exports,
   sharing, and workspace count only. (`monetization.md`.)
10. **Status is never hidden behind a generic spinner.** Engine state, run state, and reconciliation
    results are always shown honestly, always with a text label, never color alone.
11. **Orchestrator correctness is not retrofitted.** Leases with heartbeats, per-step idempotency
    keys, and retry/backoff ship with the `steps` row from day one, with dedicated tests for lease
    expiry, retry idempotency, and fork semantics. (`risk_log.md` R-005, R-012.)
12. **Tunable thresholds live in `server/internal/config`** — never as scattered literals. Currently:
    upload size ceiling (FEAT-004), reconciliation exclusion threshold (FEAT-029), engine idle
    teardown (~10 min). Values pending confirmation from the product owner.
13. Run tests and lint before declaring any unit of work complete.
14. **Commit messages are Conventional Commits**, with the feature IDs delivered listed in an
    `Implements:` footer:

    ```
    feat(ingest): implement file upload and profiling

    Implements: FEAT-004, FEAT-005
    ```

    Scope is the module or slice (`ingest`, `auth`, `engine`, `web/report`). Use `feat`, `fix`,
    `refactor`, `test`, `chore`, `docs` (code docs only — never `docs/Groundwork/`).
15. **A roadmap phase never rolls forward red.** Before the next phase starts: full test suite, lint,
    and build all green across the *whole* codebase (not just the new slice), navigation wired per
    `user_flow.md`, and the Build Progress table (§7) updated. A failing phase is fixed in place.
16. **Shared modules are written once, by the foundation/assembly pass — not by a feature slice.**
    `web/src/styles/globals.css`, `src/types/`, `src/lib/`, `internal/domain/`, `internal/config/`,
    and the route registry are read-only while implementing a feature. If a slice needs one changed,
    report the exact change instead of making it.

## 6. Document Map

| Document | Use When |
|---|---|
| `docs/Groundwork/README.md` | Understanding product concept, audience, SWOT, scope boundaries and explicit non-goals |
| `docs/Groundwork/user_personas.md` | UX decisions, onboarding depth, UI complexity — Chidinma (SQL-visible) and Tunde (SQL-never) are co-equal |
| `docs/Groundwork/branding.md` | Theme, colors, typography, spacing, dark mode, accessibility. Section 1 is the authoritative `Experience Tier` (**Standard**) and `Target Platform` (**Web + responsive mobile**) |
| `docs/Groundwork/features.md` | Implementing features, writing tests, checking acceptance criteria, reading the dependency graph |
| `docs/Groundwork/roadmap.md` | Implementation order and launch scope; its 5 phases are the build schedule — one dispatch/verify pass per phase |
| `docs/Groundwork/monetization.md` | Tier checks, feature gating, billing flows (Phase 4+) |
| `docs/Groundwork/risk_log.md` | Resilience work: orchestrator leases/idempotency (R-005), engine-loss recovery, single-region caveats, cgo build constraints (R-010) |
| `docs/Groundwork/user_flow.md` | Navigation, validation, error/offline handling. 5 flows, each with alternate paths that must be implemented |
| `docs/Groundwork/screens.md` + `screens/` | Building UI. `screens.md` is the index + shared components; each screen's full detail (states, edge cases) is in `screens/NN-*.md` |
| `docs/Groundwork/architecture.md` | Component boundaries, data stores, integrations, deployment, the Next.js/Go boundary rule |
| `docs/Groundwork/schema.md` | Data models, migrations, API contracts, type definitions. 18 entities |

## 7. Build Progress

| Phase | Features | Status |
|---|---|---|
| **A — Foundation** | Scaffold, theme tokens, schema entities, auth, routing skeleton, docker-compose | Not started |
| **1 — Foundation: Workspace, Ingest & Trust** | FEAT-001, 002, 003, 004, 005, 006, 007, 008, 009, 011, 042, 044 | Not started |
| **2 — Core Grounded Answer Loop** | FEAT-013, 014, 015, 016, 017, 018, 019, 020 | Not started |
| **3 — Trust & Observability** | FEAT-022, 023, 024, 027, 028, 029, 030 | Not started |
| **4 — Reports & Delivery** | FEAT-034, 035, 036, 041, 043 | Not started |
| **5 — Polish & Expansion** | FEAT-010, 012, 021, 025, 026, 031, 032, 033, 037, 038, 039, 040, 045, 046, 047 | Not started |

> **Current authorized scope: Foundation + Phase 1 only.** Phases 2–5 are documented but not
> authorized for implementation. Do not begin a later phase without explicit approval.

Update this table at each phase checkpoint. It is how anyone tells what exists in the codebase from
what is only documented.

## 8. Key Commands

**Docs submodule**
```bash
git submodule update --init docs      # After clone - fetches docs/Groundwork
git -C docs pull origin main          # Get the latest specs
git -C docs log --oneline -5          # What changed in the specs
git add docs                          # Stage a pointer bump (with the code it pairs with)
```

**Local infrastructure**
```bash
docker compose up -d                 # Postgres + MinIO
docker compose down -v               # Tear down, including volumes
```

**Backend (`server/`)**
```bash
CGO_ENABLED=1 go build ./...         # Build (cgo required — DuckDB driver)
CGO_ENABLED=1 go run ./cmd/api       # Run the API service
CGO_ENABLED=1 go test ./...          # Test
CGO_ENABLED=1 go test -race ./...    # Test with race detector
gofmt -l . && golangci-lint run      # Format check + lint
goose -dir migrations postgres "$DATABASE_URL" up      # Apply migrations
goose -dir migrations postgres "$DATABASE_URL" down    # Roll back one
goose -dir migrations create <name> sql                # New migration
```

**Web (`web/`)**
```bash
npm install
npm run dev                          # Dev server
npm run build                        # Production build
npm run test                         # Vitest
npm run test:e2e                     # Playwright
npm run lint                         # ESLint
npm run typecheck                    # tsc --noEmit
```
