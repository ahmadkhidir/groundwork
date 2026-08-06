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
| Auth | Auth.js (`next-auth`), Credentials provider + JWT sessions | `5.0.0-beta.32` |
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

**No database adapter in the web tier — deliberately.** `architecture.md`'s boundary rule confines
Next.js to rendering, routing, session handling, and BFF proxying. Auth.js therefore uses a
Credentials provider that verifies against the Go API's `/v1/auth/login`, with **JWT** sessions
(a database session strategy would require this tier to hold a Postgres connection). `@auth/pg-adapter`
and `pg` were removed for this reason; do not reintroduce them.

**cgo is mandatory — once the DuckDB driver lands.** `CGO_ENABLED=1` and a C compiler are required
for every Go build and test run that includes it. Fully static binaries are not available. Accepted
constraint — see `risk_log.md` R-010.

> **Current state (updated 2026-08-05):** the toolchain is **installed and verified** — MSYS2
> `ucrt64` GCC 16.1.0, `CGO_ENABLED=1`, official DuckDB 1.5.5 C library. Verified end to end
> against the real workload: CSV type sniffing, Parquet write with null preservation, and a
> persistent database file. `server/go.mod` requires `duckdb-go/v2` and the whole suite builds and
> passes with cgo on.
>
> `server/internal/engine`'s local-process DuckDB implementation landed with FEAT-004. Build through
> `make` (§8), never bare `go` commands, or the Windows link fails.

**The production image MUST preinstall the DuckDB `excel` and `sqlite_scanner` extensions.** Ingest
calls `INSTALL` for them on first Excel/SQLite use, which fetches from `extensions.duckdb.org` at
ingest time — in a locked-down production network the *first* such upload fails, with no prior
warning, even though `screens/05-file-upload.md` advertises both formats. See `risk_log.md` R-017.

**Do not substitute `spatial` for Excel.** It works — `spatial` reads xlsx through GDAL — and an
earlier revision used it via `st_read`. But it silently defeats the mitigation above: an image
preinstalling `excel` would still trigger a runtime fetch for `spatial`. It is also heavier (pulls in
GDAL) and leaks an `OGC_FID` column into ingested data. `spatial` now appears **only** in the test
that *writes* an xlsx fixture, never in the ingest path.

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

**Route map** (from `docs/Groundwork/screens.md` — 19 screens):

| Route | Screen |
|---|---|
| `/` | Landing Page |
| `/signup`, `/login` | Sign Up / Login |
| `/forgot-password` | Forgot Password (18) |
| `/reset-password` | Reset Password (19) |
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
| `space-xs`…`space-3xl` | **none — use the built-in numeric scale** | `p-4`, `gap-6` |
| `radius-sm`…`radius-full` | `--radius-sm` … | `rounded-md` |
| `shadow-sm`…`shadow-lg` | `--shadow-sm` … | `shadow-md` |

> **Naming note:** branding's color token `text-primary` becomes the utility `text-text-primary`
> (Tailwind prefixes color utilities with `text-`). This is intentional — the branding token name is
> preserved rather than renamed to something that would no longer match the docs.

**Non-token values:** `button-height` (40px), `input-height` (40px), and `icon-sm/md/lg` are sizing
constants exposed as `--size-*` custom properties and consumed via arbitrary-value utilities where no
native Tailwind slot exists.

**Skipped tokens: none.** Every token in `branding.md` is mapped, in both light and dark. If a future
token cannot be mapped, record it here with the reason. **A skipped token is never inlined as a raw
value at a call site** — report it instead.

> ### ⚠️ Never define named `--spacing-*` tokens
>
> `branding.md`'s spacing scale is a 4px grid, which **is** Tailwind's built-in numeric scale. Use it:
>
> | branding | utility | | branding | utility |
> |---|---|---|---|---|
> | `space-xs` 4px | `1` | | `space-xl` 32px | `8` |
> | `space-sm` 8px | `2` | | `space-2xl` 48px | `12` |
> | `space-md` 16px | `4` | | `space-3xl` 64px | `16` |
> | `space-lg` 24px | `6` | | | |
>
> Defining `--spacing-md`, `--spacing-3xl`, … looks harmless and **breaks every page layout**.
> Tailwind v4 resolves `max-w-*` and `w-*` against the `--spacing-*` namespace, and those key names
> (`xs`…`3xl`) are the *same keys* as the container scale, so it emits
> `.max-w-3xl { max-width: var(--spacing-3xl) }` — 64px instead of 48rem.
>
> This shipped in Foundation and survived Wave 1. **Typecheck, lint, 63 unit tests, and the
> production build all passed** while every page rendered as a ~50px sliver. It is only visible on
> screen. `src/app/globals.test.ts` now guards it.

**Two deliberate exceptions to "no literal values", both narrow:**

1. `--size-*` tokens (`button-height`, `input-height`, `icon-sm/md/lg`, `touch-target`) live inside
   `@theme` but generate no utilities, because Tailwind v4 has no `--size-*` utility namespace.
   Consume them as `h-(--size-button-height)`. The value still lives in exactly one place, which is
   the rule that matters.
2. `text-white` on the `primary` and `destructive` button fills. `branding.md` specifies a literal
   `#FFFFFF` there — it is a fixed foreground for a colored fill and must **not** flip in dark mode,
   so it is deliberately not the `surface` token.

**Component library** (`web/src/components/ui/`) — where the component tokens landed. Use these
rather than restyling from scratch:

| Component | Covers |
|---|---|
| `Button` / `buttonClassNames()` | All 5 variants. Use the helper for `<Link>`s that should look like buttons — never copy the classes |
| `StatusPill` | All 5 variants + `engineStatusPill()` / `runStatusPill()` mappers. **The label prop is required by the type** — a colored dot with no text is unconstructable |
| `Input` | All 4 states, with label binding, `aria-invalid`, and `aria-describedby` wired |
| `Card` / `CardHeader` | Surface container and standard header |

**Accessibility is in the base layer, not per-feature.** `globals.css` carries the global
`:focus-visible` ring, the 15px body floor, `prefers-reduced-motion`, and the `.touch-target`
utility for the 44px mobile minimum; `layout.tsx` carries the skip link. Inherit these; don't
reimplement them per screen.

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

   **How a browser request authenticates against Go:** the browser calls the Go API *directly* with
   `credentials: "include"`, so the only thing that travels is the Auth.js session cookie.
   `server/internal/authsession` decodes that cookie (JWE, `dir` + `A256CBC-HS512`, HKDF-derived
   key) using the shared `AUTH_SECRET`. **That decode — not `proxy.ts` — is the authorization
   boundary.** `proxy.ts` only redirects for UX. Every protected route composes
   `authsession.RequireAuth` → `requireWorkspaceOwnership`.

   Two consequences: `AUTH_SECRET` must be **byte-identical** in `web/.env.local` and `server/.env`,
   and in production both tiers must share a cookie-eligible parent domain. Local dev works
   regardless, because host-only cookies ignore the port.
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
12. **Tunable thresholds live in `server/internal/config`** — never as scattered literals:

    | Threshold | Value | Source |
    |---|---|---|
    | Upload size ceiling, per file | **500 MB** | Product owner, confirmed 2026-08-05. `features.md` FEAT-004 says only "a stated size ceiling" |
    | Engine idle teardown | ~10 min | `architecture.md` §107 |
    | Reconciliation exclusion threshold | *pending* | FEAT-029, Phase 3 |

    The upload ceiling applies **per file, across all tiers** — `monetization.md` gates usage
    allotments (engine-seconds, storage, tokens), not per-file size. It is enforced **twice**:
    client-side before the upload starts (`user_flow.md`, `screens/05-file-upload.md` §39) and
    server-side on receipt, since client validation is a UX affordance and not a control. The
    rejection message **states the 500 MB figure explicitly** — a generic "file too large" is a
    spec violation (`screens/05-file-upload.md` §54). One exported constant feeds the validator,
    the server check, and the message; the number appears in code exactly once.
13. Run tests and lint before declaring any unit of work complete. **For any change touching UI,
    that is not sufficient — actually look at the rendered page.** Start `npm run dev` and screenshot
    the affected screens in both light and dark, and check the browser console is clean. A green
    suite proved nothing about layout during Foundation: the entire app rendered as a 50px sliver
    while every automated check passed.
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
| **A — Foundation** | Scaffold, theme tokens, schema entities, auth, routing skeleton, docker-compose | **Complete** |
| **1 — Foundation: Workspace, Ingest & Trust** | FEAT-001 ✅ 002 ✅ 003 ✅ 004 ✅ 005 ✅ 006 ✅ 007 ✅ 008 ✅ 009 ✅ 011 ✅ 042 ✅ 044 ✅ 048 ✅ 049 ✅ | **Complete** — reopened for FEAT-048/049 by docs `ff8d5b2` (2026-08-06) and closed by this wave; see below |
| **2 — Core Grounded Answer Loop** | FEAT-013 ✅, 014 ✅, 015 ✅, 016 ✅, 017 ✅, 018 ✅, 019 ✅, 020 | In progress — Wave 1–4 landed (017 ✅, 018 ✅ in Wave 4); only FEAT-020 remains |
| **3 — Trust & Observability** | FEAT-022, 023, 024, 027, 028, 029, 030 | Not started |
| **4 — Reports & Delivery** | FEAT-034, 035, 036, 041, 043 | Not started |
| **5 — Polish & Expansion** | FEAT-010, 012, 021, 025, 026, 031, 032, 033, 037, 038, 039, 040, 045, 046, 047 | Not started |

> **Current authorized scope: Foundation + Phases 1 and 2** (user-approved 2026-08-05, along with
> the four phase-boundary assumptions recorded under "Phase 2 boundary assumptions" below). Phases
> 3–5 are documented but not authorized. Do not begin a later phase without explicit approval.
>
> **FEAT-019 (Semantic Layer Storage) landed in Phase 2's Wave 1**, as a solo pass (no parallel
> dispatch) — see the "FEAT-019 — Semantic Layer Storage" note below for what shipped and what it
> deliberately left for FEAT-013/FEAT-020.
>
> **FEAT-013 (Natural-Language Question → SQL Generation) landed in Phase 2's Wave 2** (this wave),
> as a solo pass — see the "FEAT-013 — Natural-Language Question → SQL Generation" note below for
> what shipped, what the linear executor persists, and what the approved phase boundary left for
> later waves.
>
> **FEAT-014 (SQL Execution & Fact Return), FEAT-015 (Narrative Generation), and FEAT-016 (Chart Spec
> Generation) landed in Phase 2's Wave 3** as a single implementation pass: the executor's pipeline
> grew to plan → generate_sql → execute → chart_spec → narrate, then materializes a self-contained
> `Report` (title, narrative, chart spec, reconciliation summary, empty provenance link array) at
> completion; the report read endpoint `GET /api/workspaces/:id/reports/:reportId` serves it. The
> row-ceiling AC lands here (`config.MaxExecuteRows`, 10k, fail-closed); the R-016 facts seam extends
> to `model.BuildFactsPayload`, which drops any PII/excluded column's VALUES from the facts before
> they reach chart_spec/narrate; `chart_spec`/`narrate` emit only parsable payloads (no chart image, no markup). Wave 3 is verification-clean (`go vet` + fresh `go test` + `gofmt`).
>
> **FEAT-017 (First-Party Chart Rendering) and FEAT-018 (SQL Editing → New Run) landed in Phase 2's
> Wave 4** as a single web+server pass. The Report View screen (`web/src/components/report/`) renders
> the self-contained report — narrative, reconciliation notice, and a first-party SVG chart
> (`ChartRenderer`: bar/line/pie/scatter/table, no charting library) drawn from the report's
> `chart_spec` over the run's execute facts, with an accessible data-table fallback; it also auto-
> navigates from a completed run to its report via the GET-run `report_id`. FEAT-018 lets the Run
> Viewer edit the generated SQL inline (`EditableCodeBlock`) and submit it: the handler routes to
> `RunExecutor.SubmitWithSQL` → `executeSeeded`, which starts a NEW run whose `generate_sql` step is
> seeded with the user's exact edited SQL (no plan step, no model call), then continues
> execute → chart_spec → narrate → report. Per the approved FEAT-024 boundary, editing never amends
> or resumes — it always starts a fresh run. Wave 4 is verification-clean: server `go vet` + fresh
> `go test` (cgo env) + `gofmt` clean; web `vitest` 169/169 + `eslint` + source-only `tsc --noEmit`.

### Phase 1 reopened for FEAT-048 / FEAT-049 (docs `ff8d5b2`, user-approved 2026-08-06) — CLOSED

Phase 1 was reopened with two P0 features (FEAT-048 Single-Dataset Deletion, FEAT-049 Duplicate
Filename Detection & Resolution at Upload), plus a same-filename exclusion added to FEAT-008. Both
are implemented and Phase 1 is closed. Verification: server `go vet` + `go test` (fresh) + `gofmt`
clean, web `tsc --noEmit` + `vitest` 138/138 + `eslint` (0 warnings) + `next build` all green. This
is the pass that delivered the reopening.

The docs never said what happens when a file is uploaded to a workspace that already contains one.
A user hit it: an ingest-progress bug (now fixed, `ef02198`) reported failure for uploads that had
succeeded, they retried, and one workspace ended up with 5 identically-named datasets, 64 columns
and **216 candidate joins** from comparing every copy against every other. The confirmation screen
listed the same 16 columns four indistinguishable times, and nothing in the product could remove
any of it — there is no dataset delete, and FEAT-043 (workspace delete) is Phase 4. The workspace
had to be destroyed by hand, against Postgres and MinIO directly.

- **FEAT-048 — Single-Dataset Deletion (S).** Must delete database rows **and** both storage objects
  (original + normalized Parquet). Verified during the manual cleanup: the database FKs cascade on
  their own, object storage does **not**. An implementation that relies on the cascade will leave
  every uploaded file behind while telling the user their data is gone.
- **FEAT-049 — Duplicate Filename Detection (M).** An exact `original_filename` match in the same
  workspace requires an explicit Replace / Keep both choice before ingest. Applies to a `failed`
  dataset being retried — the case that caused the incident.
- **FEAT-008 changes with them:** no candidate joins between datasets sharing a filename, and the
  skip is shown on the confirmation screen rather than silently producing zero candidates.
- **Replace discards that dataset's type overrides and PII decisions.** Accepted for Phase 1
  (FEAT-019 owns cross-ingest replay, Phase 2), but the confirmation must say so. Risks `R-018`
  and `R-019` record both halves.

Screens 04 (new Datasets panel), 05 (duplicate dialog), and 07 (per-dataset grouping, skip notice)
were built against the older spec and are now behind it.

**What shipped this wave (all against docs `ff8d5b2`):**

- **FEAT-048 — `server/internal/api/datasets.go`** (new): `GET /v1/workspaces/:id/datasets`,
  `GET .../datasets/:id/deletion-impact`, `DELETE .../datasets/:id`, plus the exported
  `DeleteDatasetAndObjects` used directly by FEAT-049's Replace path. Deletion order is deliberate
  and tested: **object storage first, DB row second, audit entry third** — a storage failure leaves
  the dataset intact and retryable. Both objects (original + normalized Parquet, when one exists)
  are removed by explicit `storage.Client.DeleteObject` calls; the FK cascade (`migration 00001`)
  handles the DB half. `audit.Writer.WriteDeletion` records `action_type: deletion` per FEAT-042.
  Deletion impact names dependent Relationships via `RelationshipRepository.ListByDatasetColumns`.
- **FEAT-048 — `web/src/components/workspace/DatasetsPanel.tsx`** (new) on Workspace Home: lists
  filename / `created_at` / row count / `ingest_status`, per-row delete, `readOnly` hides the
  delete action (FEAT-045), and the shared `DeleteConfirmationModal` (new,
  `web/src/components/shared/DeleteConfirmationModal.tsx`) states the explicit scope + the
  review-work-lost warning before commit. Deleting the workspace's last dataset reverts
  `has_datasets` locally (screens/04 Data Flow step 6).
- **FEAT-049 — `GET /v1/workspaces/:id/files?filename=`** (`upload.go`'s
  `RegisterDuplicateCheckRoute`): exact `original_filename` match against every dataset, any
  `ingest_status` including `failed`. Client flow (`web/src/components/upload/`): every selected
  file passes `checkDuplicateFilename` → "checking-duplicate" / "duplicate" row states; a collision
  opens `DuplicateFilenameDialog` (Replace pre-selected, never auto-applied; Keep both leaves the
  prior dataset untouched); Replace calls the FEAT-048 DELETE first, then uploads fresh; Start
  Ingest stays disabled until every collision resolves.
- **FEAT-008 — same-filename exclusion in `internal/ingest/pipeline.go`**: two datasets sharing an
  `original_filename` are never compared (unconditional — closes the 216-candidate-join incident
  mechanism). The skip is surfaced on Ingest Confirmation's Relationships step as a
  `skipped_pairs` notice (`profiling.go` `computeSkippedFilenamePairs` computed at read time, not
  persisted), naming the pair by filename + `created_at`, with a pointer to the Semantic Layer.
- **screens/07 — per-dataset grouping**: Types and Sensitive Columns tables now always group by
  source dataset with a sticky sub-header naming `original_filename` + `created_at`
  (`groupColumnsByDataset` / `datasetGroupLabel` in `confirmation/types.ts`), so two "Keep both"
  datasets sharing a name are never an undifferentiated flat list — the exact incident failure mode.
- **Tests**: `datasets_test.go` (ownership boundary, impact disclosure naming relationships +
  reviewed-PII, both-object deletion, failed-dataset duplicate detection, storage-failure abort
  ordering, cross-workspace rejection), `profiling_test.go` (skipped pairs), `pipeline_test.go`
  (same-filename pair never produces a join even with identical content), `DatasetsPanel.test.tsx`
  (6 cases incl. read-only + inline error retry), `UploadScreen.test.tsx` (dialog auto-shows,
  failed-dataset retry, Replace deletes via FEAT-048), `IngestConfirmationScreen.test.tsx` (skip
  notice + manual-add link).

### Post-closure bugfix — type-override confidence badge (FEAT-006), committed after the closure wave

A user report exposed a real lying-badge regression on the Ingest Confirmation "Types" step: after
overriding a column's type (e.g. `ACCT_DATE_OPEN` to `Date`), the confidence badge still showed
**100% / HIGH** even though the newly selected type could not be 100%. Root cause was two-pass:

1. **Server** — `handleUpdateColumn` (`confirmation.go`) set `col.TypeConfidence = 1.0` on the
   in-memory copy before building the PATCH response, fabricating "100% of values fit this type".
   The database write (via `ColumnRepository.UpdateInferredType`, the COALESCE in `columns.go`) was
   already correct — it read the newly selected type's measured fit rate. The response lied while the
   DB told the truth, and the client merged the response.
2. **Client** — `TypeReviewStep` read the badge only from `col.type_confidence`, never from
   `type_fit_rates`, and had no in-flight selection state; `type_fit_rates` was in no API response
   and in no client type. So even a correct server could not satisfy FEAT-006's "badge updates in the
   same interaction" requirement.

Docs statement this violated (exact): features.md FEAT-006 "Overriding a type updates the confidence
badge, not just the dropdown… the badge updates immediately — in the same interaction, no loading
state — to show the precomputed fit rate for the newly selected type"; screens/07 "The badge rendered
for a column is `type_fit_rates[override ?? inferred_type]`."

**Fix:**

- **Server** — the override handler now mirrors the SQL's COALESCE: move `TypeConfidence` to the
  newly selected type's measured fit rate from `TypeFitRates`; a pre-00006 column with no rates keeps
  its existing confidence. `type_fit_rates` is now emitted on `publicColumn` (profiling.go),
  `publicConfirmationColumn` (confirmation.go list), and the PATCH response (via `toPublicColumn`).
- **Client** — `ConfirmationColumn` and schema `Column` carry optional `type_fit_rates`; the badge
  reads `typeFitRate(col, active)` (fit-rate lookup, COALESCE-consistent fallback). `TypeReviewStep`
  keeps per-row pending-override state so the badge + dropdown update in the same interaction, and
  reverts to the server-backed value when the save fails.
- **Tests** — the old `TestUpdateColumnOverridesInferredType` asserted the buggy `1.0`; it (and the
  fake store, which mirrored the bug) now assert the measured fit rate plus a pre-migration
  COALESCE-fallback case; client tests add same-interaction badge update (PATCH never resolves) and
  a failure-revert assertion, and drop the fabricated `type_confidence: 1`.

Verification: server `go vet` + fresh `go test` + `gofmt -l` clean; web `tsc --noEmit` + `vitest`
139/139 + `eslint` (0 warnings) + `next build` all green.

**Reported doc/architecture ambiguities from this wave (routed to product-shaper, not fixed):**

1. **`Column.type_overridden_at` does not exist in schema.md.** FEAT-048 requires the confirmation to
   state when a dataset "has any columns carrying a type override," but `Column`'s only type field is
   `inferred_type`, overwritten in place by a FEAT-006 override — indistinguishable from the original
   profile. `deletion-impact` therefore returns `has_type_override_risk: true` whenever a dataset has
   any columns (deliberate over-disclosure, documented in `datasets.go`; a false negative would
   violate the acceptance criterion outright). A `type_overridden_at` field would make it exact.
2. **Datasets panel platform scope.** `screens/04`'s Components table says "Both (collapsible on
   mobile)" but its mobile Layout section does not list the panel; the implementation shows it in the
   web two-column layout only. Not built on mobile; needs a doc decision.
3. **Duplicate detection is client-flow only on the upload path.** The required resolution prompt is
   inherently UI, and the docs' defense-in-depth is the server-side FEAT-008 exclusion (a bypassed
   prompt still cannot produce same-filename candidate joins, and FEAT-048 cleans up any duplicate
   that slips through). Unlike the size ceiling, the docs do not mandate a second server-side
   duplicate control; noted rather than invented.

**Environment note:** `server/Makefile`'s `fmt`/`doctor` recipes use POSIX shell syntax; on Windows
`make check` fails at the `fmt` step under cmd (`'out' is not recognized`). Run `make vet` + `make
test` and check `gofmt -l .` manually instead. Pre-existing, not introduced by this wave.

### Post-closure bugfix — dd-mmm-yy CSV dates read as text (FEAT-003), committed after the closure wave

A user's CSV carried an `ACCT_LEVEL_DATE` column of Excel-style `dd-mmm-yy` values (`29-Aug-18`,
`1-Nov-18`). DuckDB's `read_csv` sniffer does not recognize that format at all, so it typed the
column VARCHAR; profiling inferred Text; the confidence badge showed the floored 40% (`string` fit
0.4); and date math on the column failed at query time. Nothing in the pipeline could parse the
format — the entire failure traced to the loader, not to profiling or the confirmation screen.

**Fix (Option C, pipeline-level):** `normalizeCSV` (`server/internal/ingest/loaders.go`) now, after
the sniffer's whole-file read, scans any column the sniffer typed VARCHAR and converts the column to
a real DATE in the normalized Parquet when **every non-null value** parses via
`TRY_STRPTIME(col, '%d-%b-%y')` / `'%d-%b-%Y'` (COALESCE over both, so a column mixing two- and
four-digit years converts completely; `%b` matches month names case-insensitively; `%y` two-digit
year pivot is strptime's 00-68→2000s/69-99→1900s; `strptime` returns TIMESTAMP, so the result is
explicitly `CAST(... AS DATE)`). The conversion is deliberately conservative, matching
`normalizeExcel`'s philosophy: any value that does not parse keeps the whole column VARCHAR — never
silently nulled — so the review step still sees the ambiguity (a low text fit rate) instead of
quietly losing data. Columns the sniffer typed correctly (numeric, ISO dates) are never touched, and
a CSV with no VARCHAR columns costs nothing beyond the DESCRIBE. Verified against the user's exact
shape: `ACCT_LEVEL_DATE` now profiles as `date` at fit 1.0 end to end.

- **Docs basis:** `features.md` FEAT-003 (ingest normalizes the file into a canonical form) and
  `schema.md` (line 129: exact coercion rules are implementation-defined). No docs change required —
  the normalization happens inside the loader, before profiling, so inference/fit/badge/query-time
  date math all agree.
- **Tests:** `TestNormalizeCSVConvertsDDMonYYDates` (mixed two-digit, four-digit, single-digit-day,
  lowercase-month values → DATE column, values preserved exactly) and
  `TestNormalizeCSVKeepsAmbiguousColumnsAsText` (an unparseable value, an all-empty column, and a
  phone column with a leading zero all stay VARCHAR, values preserved) in `loaders_test.go`, against
  the real-engine fixture pattern.

Verification: server `go vet` + fresh `go test ./...` (all packages green) + `gofmt -l` clean.

### Post-closure bugfix — Excel date columns lost to the text fallback (FEAT-003), committed after the CSV fix

The dd-mmm-yy CSV fix above handled text dates in CSV. A follow-up on the user's real file (an `.xlsx`)
exposed a different, more subtle path. That file stores dates as **Excel serials** (43341) in cells
whose style is the date format `d-mmm-yy`. DuckDB's `read_xlsx` honours that cell-format metadata and
reads them as real `DATE` columns — but the file also has a genuinely mixed-type `MOBILE_NO` column
(one text `002-2318820` in an otherwise numeric column), which makes the whole typed read throw, and
the existing all-text fallback then cannot see the cell formats, reads the serials as strings, and
types the date columns as whole numbers. Net effect: `ACCT_DATE_OPEN`/`TRA_DATE` profiled as integers
("Excel presents it as a date" — actually the file declares it).

**Fix (hybrid fallback) in `normalizeExcel` (`server/internal/ingest/loaders.go`):** when the plain
typed read fails, retry it with `ignore_errors=true` (succeeds, keeps the file's date cells as real
`DATE`s, NULLs only the cells that caused the failure), then merge: `DATE`/`TIMESTAMP`/`BOOLEAN`
columns whose typed read dropped **zero** values come from the typed read; every other column comes
from the all-text read through the existing `excelColumnType` round-trip — so the mixed-type cell that
forced the fallback is preserved verbatim as text, not nulled. The two reads are joined by
`row_number() OVER ()` (both scan the same sheet in the same order; the pairing is verified by
`TestNormalizeExcelHybridRead` against a fixture with a real `styles.xml`). When no metadata column
survives cleanly, it falls back to the prior text-only path unchanged.

- **Verified against the user's file:** `ACCT_DATE_OPEN` → date (fit 1.0), `TRA_DATE` → date (fit
  1.0), `MOBILE_NO` → string (all values preserved, `002-2318820` intact).
- **Tests:** `excel_hybrid_test.go` `TestNormalizeExcelHybridRead` (date column stays `DATE` while the
  mixed-type cell survives; clean integer column keeps its BIGINT narrowing; mixed column stays
  VARCHAR). Existing Excel tests unchanged and green.

Verification: server `go vet` + fresh `go test ./...` (all packages green) + `gofmt -l` clean.

### Phase 2 boundary assumptions (user-approved 2026-08-05)

Four Phase 2 features lean on capabilities `roadmap.md` schedules for Phase 3+. These are the
agreed Phase 2 behaviours — each is a deliberate boundary, not a gap to be quietly filled:

| Forward reference | Phase 2 behaviour |
|---|---|
| FEAT-023 live event stream (P3) | Run Viewer **polls** run/steps. A stream replaces the transport later, not the model. |
| FEAT-022 resumable DAG (P3) | A **linear persisted executor** writing real `runs`/`steps` rows per schema.md. No leases, heartbeats, or forks. |
| FEAT-024 halt/amend/resume (P3) | FEAT-018's edited SQL starts a **new run** carrying the user's SQL as `generate_sql` output. |
| FEAT-033 honest uncertainty (P5) | An ambiguous question **fails the run with a plain message** — FEAT-013's AC forbids a best-effort guess. |

`Report` requires `provenance_links` and `reconciliation_summary`, but FEAT-029/030 produce them in
Phase 3. Phase 2 persists an empty links array and a `rows_out` the executor actually measures —
never a fabricated `rows_in`.

**Model provider:** Anthropic Go SDK, model `anthropic.ModelClaudeHaiku4_5` (`claude-haiku-4-5`),
chosen for cost and changeable in one place. `ANTHROPIC_API_KEY` comes from the environment via
`internal/config` — never hardcoded, never committed.
>
> **Phase 1 is functionally complete as of Wave 5** — every listed feature is implemented and green.
> A full pipeline pass (upload → provisioning → loading → profiling → PII flagging → relationship
> inference → ingest confirmation → workspace home) has been manually verified end to end against
> real Postgres/MinIO/DuckDB, including a re-ingest of the same two files confirming a rejected
> candidate join is not resurrected. See Wave 5's open items below for the few gaps this wave
> reported rather than invented a fix for.

**What Foundation delivered** (green: `gofmt`, `go vet`, `go test`, `tsc`, `eslint`, `vitest` 37/37,
`next build`):

- `web/` — Next.js 16 App Router. All 15 routes from `screens.md` registered; unbuilt screens render
  an honest `PlaceholderScreen` naming their spec, feature IDs, and phase. Global `error`,
  `not-found`, and `loading` states.
- `web/src/types/schema.ts` — all 18 entities, enums as const objects, empty-string-absence helpers.
- `web/src/lib/` — `routes.ts` (single route map + public-route list), `api-client.ts` (`ApiError`,
  SSE helper), `limits.ts` (500 MB ceiling), `cn.ts`.
- `web/src/auth.ts` + `web/src/proxy.ts` — Auth.js and the optimistic auth gate.
- `server/` — Go service: `config` (thresholds), `domain` (18 entities), `api` (chi router,
  request-id logging, CORS, 501s naming the owning feature), `engine` (Provider seam interface).
- `server/migrations/00001_init.sql` — all 18 tables, `workspace_id` everywhere, cascade FKs, step
  lease columns from day one, `deletion_records` deliberately outside the cascade.
- `docker-compose.yml` — Postgres + MinIO with healthchecks and bucket bootstrap.

**Placeholders are replaced, not wrapped.** A feature slice deletes the `PlaceholderScreen` call and
the route's 501 handler; it does not build around them.

### Open items

The docs pass at `c2bc326` closed four more: FEAT-005's min/max contradiction (dropped from the
description — explicitly *not* computed in Phase 1), screen 04's unassigned semantic-layer panel
(now FEAT-019), the Excel/SQLite network dependency (now `risk_log.md` R-017, with the code
realigned to the extensions it names), and screen 04's unconditional Upload button (`has_datasets`).
The PII-override distinguishability item is closed in code by `pii_reviewed_at` + the `pii_override`
audit action.

Wave 3's first two items are **closed** by Wave 4: the ingest pipeline now runs through to
`ingest_status = "complete"`, and "Profiling columns" reflects real work. Wave 4's PII-safe-defaults
item is **closed** by Wave 5: PII detection is real (`internal/pii`). The R-016 item below is
**closed** by Phase 2's Wave 2 (FEAT-013): the payload builder is now exercised by every model call
at runtime, with wire-level enforcement tests and an egress-before-call test — see the FEAT-013 note.

| Item | Detail | Owner |
|---|---|---|
| **FEAT-048 deletion-impact cannot distinguish a type override from the original profile — `Column` has no `type_overridden_at`** | `deletion-impact` returns `has_type_override_risk: true` for any dataset with columns (deliberate over-disclosure, never a false negative). An exact answer needs a `Column.type_overridden_at` field (paired with `pii_reviewed_at`'s convention) — a schema/migration decision. | Product |
| **`type_confidence`'s scoring heuristic is undocumented** | FEAT-008 now specifies the join-confidence bands and the value-overlap-only cap, and declares the weighting implementation-defined — that half is closed. `profiling.go`'s `confidenceBaseByType` still produces `type_confidence` by an undocumented rule, though `features.md` now specifies the HIGH/MEDIUM/LOW bands it is presented through. Lower stakes than the join case, but still a trust-facing number set by implementation. | Product |
| **FEAT-008's "near-unique" exemption can swallow the cap it sits beside** | The value-overlap-only cap exempts identifier-shaped columns, and `features.md` defines that to include near-unique (≥0.95) columns. A money column with all-distinct values — common in real exports — therefore reads as identifier-shaped and escapes the cap. Observed live: `total` ↔ `amount` at 40 distinct values over 40 rows scored 0.75 with basis "one side is nearly unique, consistent with a key". **No acceptance criterion is violated** (it stayed below HIGH), and the code implements the doc exactly as written rather than narrowing it. If the intent was that a measure-shaped name never escapes on distinctness alone, the rule needs a name-based override. | Product |
| **FEAT-006's "type corrections... used for all subsequent ingest of the same schema shape" is NOT implemented** | `schema.md`'s `Column` has no field keying a correction to a reusable "schema shape" (e.g. a hash of column names+types), and nothing else in `docs/` defines what a schema shape is. Implemented instead: overriding a column's type persists on that specific column (`PATCH /v1/workspaces/:id/columns/:columnId`), which is everything the schema supports today. A future re-ingest of the same file gets a fresh Column row with the ORIGINAL heuristic type, not the prior correction. Needs a schema-shape-keyed corrections table (+ migration) before this acceptance criterion can be met — route to product-shaper for the data model, since nothing was invented here. | Product |
| ~~**FEAT-007's "name-and-type-only exposure to the model" has NO ENFORCEMENT POINT**~~ — **Closed in Phase 2 Wave 2 (FEAT-013)** | Detection, user override, `pii_reviewed_at`, and the `pii_override` audit entry made a reviewed-and-cleared column distinguishable from a never-flagged one; FEAT-013 (this wave) made `pii_flag`/`excluded_from_model` govern egress at runtime by routing every model call through the one shared `model.BuildPayload` → `model.Client.Complete` seam, which reduces any flagged/excluded column to name-and-type only. The old gap text ("no payload builder exists until FEAT-013") no longer holds: `internal/model/payload_test.go` pins the wire-level filter and `client_test.go` pins egress-before-call (fail-closed), closing `risk_log.md` R-016. | **done** |
| **FEAT-009's accepted joins land directly in `relationships`, not a full semantic layer** | FEAT-019 (Semantic Layer Storage — listing, editing, using relationships as query-planning grounding) is Phase 2 and not authorized this wave. `internal/store/postgres.RelationshipRepository` is deliberately `Create`-only; there is no read/update/delete surface, and no UI beyond ingest confirmation shows a relationship once created. This is intentional under-scoping, not a gap. | FEAT-019 (Phase 2) |
| **Rejected-join resurrection is guarded by (dataset filename, column name), not a content hash** | `CandidateJoinRepository.ExistsRejectedForNamePair` matches on original filename + column name, since nothing in `docs/` defines a stronger notion of "the same file" across re-ingests. Renaming a file, or uploading a genuinely different file that happens to share both a name and a column name, are both outside this heuristic's guarantee. Verified live against a real re-ingest (see Wave 5's report) that the literal scenario in FEAT-009's acceptance criterion works. | Product (if a stronger identity is wanted) |

### Known divergences — code behind docs

Three items where the specs were corrected *after* FEAT-001 shipped, so the code is knowingly behind.
Each is a real task, not a note to ignore. Close them in the wave that owns the surrounding work.

| Divergence | Spec | Owner |
|---|---|---|
| Sign Up form has no optional **Display Name** field; the server derives it from the email local-part | `screens/02` now specifies the field, with the derived value as the blank-default | Any wave touching auth screens |
| ~~No `login` audit entries~~ — **closed in Wave 2** by migration `00002` + the audit hook in `auth.Service` | — | done |
| Password reset tokens live in a **process-local in-memory store**; delivery is a log line | `PasswordResetToken` entity + Transactional Email Provider now specified | Deferred — needs a vendor decision and a migration |

The reset-token store does not survive a restart or a multi-instance deploy. It is flagged as interim
in `server/internal/auth/reset_store.go`, `ForgotPasswordForm.tsx`, and `ResetPasswordForm.tsx`.
`schema.md` also now specifies storing a **hash** of the token rather than the token itself — the
current implementation does not, which is a second reason this needs revisiting before production.

Update this table at each phase checkpoint. It is how anyone tells what exists in the codebase from
what is only documented.

### Known divergences — dev environment vs. production

| Divergence | Detail |
|---|---|
| **Windows dev links DuckDB dynamically; Linux/CI/production link statically** | Windows builds use `-tags duckdb_use_lib` against the official DuckDB 1.5.5 DLL (§8). Linux uses the static libraries bundled in `duckdb-go-bindings`. |

Accepted deliberately on 2026-08-05 — the static path **cannot** link under any MSYS2 toolchain, for
a reason no install fixes (see §8). **Only the linkage differs.** The DuckDB version is identical on
both sides — 1.5.5, matching `duckdb-go v2.10505.0` — because the dev library is pinned to the exact
version the driver targets rather than to whatever a package manager happens to ship. Keep it that
way: if the driver is upgraded, bump `DUCKDB_DIR` in `server/Makefile` and re-download to match, or
you reintroduce a skew that passes locally and fails in CI.

The Windows binary loads `duckdb.dll` at run time. Without it on PATH the process exits **127**
(or `0xc0000135`) with no output at all — a silent failure that resembles a crash, not a missing
library. `make` puts it on PATH; running the binary by hand needs it there too.

### FEAT-019 — Semantic Layer Storage (Phase 2, this wave)

**What shipped:**

- `server/internal/store/postgres/relationships.go` — extended (Phase 1's Create-only
  `RelationshipRepository`) with `ListByWorkspace`, `GetByIDForWorkspace`, and `Update`
  (cardinality/grain_notes). Same `relationships` table FEAT-009 already writes into; no new table.
- `server/internal/store/postgres/metric_definitions.go` (new) — full CRUD over
  `metric_definitions` (already a table since migration `00001`, unused until now).
- `server/internal/store/postgres/corrections.go` (new) + `server/migrations/00005_semantic_layer.sql`
  (new table, the only new table this wave — `corrections`, with `originating_run_id` FK
  `ON DELETE SET NULL`) — Create + read side only; FEAT-020 (later wave) captures rows.
- `server/internal/store/postgres/semantic.go` (new) — `SemanticSummaryRepository`, cheap counts for
  the workspace-home summary panel.
- `server/internal/semantic/grounding.go` (new package) — `AssembleGroundingContext`, the exported,
  fully-tested building block FEAT-019 AC2 requires. **Not called by anything yet** — FEAT-013
  (Phase 2, next wave, the first feature to build a model payload) is the intended caller. Signature:
  `AssembleGroundingContext(ctx, workspaceID, RelationshipLister, MetricDefinitionLister, CorrectionLister) (GroundingContext, error)`,
  returning `{WorkspaceID, Relationships []RelationshipGrounding, Metrics []MetricGrounding, Corrections []CorrectionGrounding}`.
  Deliberately excludes `Correction.OriginalLogic` from the payload shape (see the package doc).
- `server/internal/api/semantic.go` (new) — `GET/PATCH .../semantic-layer/relationships[/:id]`,
  full CRUD `.../semantic-layer/metrics[/:id]`, `GET .../semantic-layer/corrections`,
  `GET .../semantic-layer/summary`. Wired into `api.go`'s `Dependencies` and `cmd/api/main.go`.
- `web/src/components/semantic/` (new) — `SemanticLayerScreen`, `RelationshipsTab` (+
  `RelationshipEditor`), `MetricDefinitionsTab` (+ `MetricDefinitionEditor`), `CorrectionsTab`.
  Replaces the screen 15 placeholder. `web/src/components/workspace/SemanticLayerSummaryPanel.tsx`
  now calls the real summary endpoint instead of showing "Not built yet".

**Reported doc gaps/assumptions (routed to product-shaper, not fixed in code):**

- `screens/15-semantic-layer.md`'s Relationships tab lists "confidence, status (confirmed/rejected)"
  as columns, but schema.md's `Relationship` entity has neither field (those belong to
  `CandidateJoin`). Implemented using only what `Relationship` actually carries; every row is
  definitionally "confirmed" since a `Relationship` only exists once accepted.
- Metric definition **creation** (not just editing) was built as an in-scope assumption under this
  wave's "full CRUD" grant — screens/15 documents editing an existing definition explicitly but shows
  no "add new metric" affordance in as many words. Flagged in `createMetricDefinitionRequest`'s doc
  comment in `semantic.go`.

**Left unimplemented, on purpose:** any model call, run, or step consuming the grounding context
(FEAT-013, next wave); capturing a correction during a run, and editing/removing a stored correction
(FEAT-020, later wave — the Corrections tab's Empty state is therefore expected, not a bug).

### FEAT-013 — Natural-Language Question → SQL Generation (Phase 2, this wave)

**What shipped:**

- `server/internal/model/client.go` (new) — the only route to Anthropic. `Client.Complete` accepts
  the sealed `model.Payload`, renders the exact bytes via `Payload.JSON()`, writes the egress entry
  through `audit.Writer.WriteModelCall` **before** the outbound call and **fails closed** if the log
  write fails, then sends via the official SDK (`github.com/anthropics/anthropic-sdk-go` **v1.61.0**,
  model `claude-haiku-4-5`, `DefaultMaxTokens 4096`). Missing `ANTHROPIC_API_KEY` fails per-call with
  a clear error, never at boot.
- `server/internal/run/` (new) — the linear persisted executor (`plan → generate_sql → execute`).
  Persists real `runs`/`steps` rows with per-step idempotency keys (`runID:stepName`), reports
  `waking` before `Acquire` blocks, and marks the run `complete` only after execute persists its
  facts. `Answerable=false` from the plan step **fails the run with the plan's plain-language
  reason** (FEAT-033 boundary). Every model payload flows through `semantic.AssembleGroundingContext`
  + `model.BuildPayload` (the R-016 seam, now exercised at runtime). Engine views are attached per
  dataset under the canonical table name `dataset_<sanitized-filename-stem>_<id8>`, which is exactly
  the identifier taught to the model — the SQL handoff is consistent by construction.
- `server/internal/store/postgres/runs.go` + `steps.go` (new) — `RunRepository`/`StepRepository`
  over the tables migration `00001` already created; no migration needed this wave.
- `server/internal/api/runs.go` (new) — `POST /v1/workspaces/:id/runs` (202, starts execution in the
  background — runs are server-side state, the UI is a viewer) and `GET /v1/workspaces/:id/runs/:runId`
  returning the run with its step feed (the approved **polling** contract; no SSE this wave). Ownership
  enforced (404 cross-workspace); read-only members cannot trigger runs. Wired via the usual
  `Dependencies` seams + `RegisterRunRoutes` guard in `api.go` and `cmd/api/main.go`.
- `server/internal/config/config.go` — `AnthropicAPIKey`, **optional at Load** (user-approved
  deviation: a keyless dev environment boots and serves everything except model calls; `main.go`
  logs a warning when unset).
- `web/src/components/run/` (new) — `RunViewerScreen` (1s polling until terminal; waking notice;
  clarification banner for unanswerable runs; view-only SQL with "SQL editing arrives with FEAT-018"
  hint — no halt/amend controls, per the phase boundary), `StepFeed` (step list in `PipelineOrder`,
  icon per status, failed-step reason), `ResultTable` (facts with null → "—", empty state),
  `step-payloads.ts` (per-step parsers). Plus `shared/CodeBlock.tsx` (new, read-only, themed).
- `web/src/components/workspace/AskPanel.tsx` — rewritten from the honest disabled placeholder to
  live submission (trim, POST → 202 → navigate to `routes.run(...)`, loading/error/disabled states).
  `WorkspaceHomeScreen` now renders it; the Run Viewer route page replaces its `PlaceholderScreen`.

**Boundaries honoured (approved 2026-08-05):** linear executor (no leases/heartbeats/forks/resume);
polling viewer (no stream); unanswerable → failed run with plain reason; **as of this wave** the
pipeline ended at `execute` with no Report row (FEAT-034 Phase 4) and an unbounded row count — both
were then extended by Wave 3: `chart_spec`/`narrate` and Report materialization (row ceiling
`config.MaxExecuteRows`) landed there, so this note reads as the Wave 2 snapshot, not current state.
SQL visible but not editable (FEAT-018 next wave); static_check/reconcile/shadow_check/
adversarial_review are Phase 3/5 and still deliberately absent.

**Assumptions / notes:**

- `ingest.Pipeline.downloadToTemp` is unexported, so `internal/run` mirrors the download-to-temp
  Parquet pattern internally (documented in `prepareViews`). If a later phase wants one shared
  helper, `ingest` should export it — no change made this wave.
- Only `ingest_status = complete` datasets are queryable; a workspace with none fails the run
  honestly at the plan step.
- The egress log now carries real `model_call` entries (plan + generate_sql) and `query_executed`
  entries for executed SQL, in the same code path as each action.

**Tests:** `payload_test.go` (R-016 wire-level pin: PII/excluded columns reach the wire name+type
only), `client_test.go` (egress-before-call, fail-closed on key/audit error), `run_test.go`
(happy path, not-answerable, engine-unavailable, garbage plan output, no-complete-datasets, table
naming/identifier sanitization/value conversion), `api/runs_test.go` (create/get, cross-workspace
404s), web `RunViewerScreen.test.tsx` (polling stop, states) and `AskPanel.test.tsx` (submit,
navigation, disabled/error). Verification: server `make vet` + fresh `make test` (all packages) +
`make build` + `gofmt -l` clean; web `vitest` 149/149 + `eslint` + `tsc --noEmit` + `next build` all
green.

### FEAT-014 / FEAT-015 / FEAT-016 — SQL Execution, Narrative, Chart Spec + Report materialization (Phase 2, Wave 3)

**What shipped:**

- `server/internal/run/run.go` (extended) — the executor pipeline grew to
  `plan → generate_sql → execute → chart_spec → narrate`, then materializes a self-contained
  `Report` at completion before marking the run `complete`. The row-ceiling AC from FEAT-014 lands
  here: a result set over `config.MaxExecuteRows` (10,000, user-confirmed 2026-08-06) fails the
  execute step with an honest message — never a silent truncation. The reconciliation summary is
  measured at execute time: `rows_out` is the executed `RowCount`, `rows_in` is the summed
  `count(*)` over every known dataset view the SQL references (name-matched via the same
  `tableNameForDataset` convention the model is taught), with an explicit exclusion warning when no
  known view can be matched (rows_in = 0 rather than a fabricated number). `chart_spec` runs before
  `narrate` per architecture.md's `PipelineOrder`; the narrate step's input is the execute output
  JSON (FEAT-015's literal contract), and both facts-consuming steps receive their payload only
  through `model.BuildFactsPayload`.
- `server/internal/model/payload.go` (extended) — `BuildFactsPayload`: the same sealed construction
  seam as `BuildPayload`, plus the executed result set. Any result column whose `PIIFlag` or
  `ExcludedFromModel` is set in the workspace metadata has its VALUES dropped (column index removed
  from both the column list and every row, order and row width preserved) before the payload is
  sealed — the R-016 value-withholding rule now covers facts, not just metadata. The payload wire
  shape is unchanged except an optional `facts` key that metadata-only steps never carry.
- `server/internal/domain/run.go` (extended) — `ChartSpecStepOutput`, `NarrateStepOutput` step
  output shapes (the domain `ChartSpec`, `Report`, and `ReconciliationSummary` types already existed
  in `domain.go`; no schema change — the `reports` table was in migration `00001`).
- `server/internal/store/postgres/reports.go` (new) — `ReportRepository`: `Create` (JSONB
  chart_spec/reconciliation_summary; provenance deliberately not written — FEAT-030 Phase 3) and
  workspace-scoped `GetByID` (a cross-workspace id reads as not-found in SQL).
- `server/internal/api/reports.go` (new) — `GET /v1/workspaces/:id/reports/:reportId` serving the
  fully self-contained report (ownership enforced, 404 non-enumeration). Wired via
  `Dependencies.Reports` + `RegisterReportRoutes` in `api.go`, and `ReportRepository` in
  `cmd/api/main.go`.
- `server/internal/config/config.go` — `MaxExecuteRows = 10000` as the single config knob for the
  row ceiling (threshold in config, never hardcoded at a call site).

**Boundaries honoured:** `chart_spec`/`narrate` emit only parsable payloads (no chart image, no
markup; the chart prompt forces token-compatible encodings — categorical series for the
`chart-cat-1..8` tokens, directional series for `chart-div`, `table` when a chart adds no clarity).
`ProvenanceLinks` stays an empty array (FEAT-030 Phase 3). Report persistence failure fails the run
— a corrupt state is never reported complete. Row measurement is a bounded identifier scan, not a
SQL parser.

**Tests:** `run_test.go` (happy path now 5 steps + report materialization with reconciliation
assertions; row-ceiling failure; chart_spec garbage → run failed; narrate garbage → run failed with
no report; report-persistence failure → run failed; `measureRowsIn` matched/unmatched + exclusion
warning; `deriveReportTitle` truncation), `model/payload_test.go` (BuildFactsPayload drops PII and
excluded VALUES from facts; facts key absent on metadata-only payloads; wire shape), `api/
reports_test.go` (auth, 200, unknown/cross-workspace/non-owner 404s), `config_test.go`
(`MaxExecuteRows` pin). Verification: `make vet` + fresh `make test` (all packages) + `make build` +
`gofmt -l` clean.

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

**Use `make`, not bare `go` commands.** From FEAT-004 the DuckDB driver requires cgo, and Windows
requires different linkage than Linux. `server/Makefile` encodes that; a bare `go build` on Windows
fails at link time with an error that reads like a compiler bug (see below).

```bash
make doctor                          # Diagnose the toolchain — run this FIRST if a build fails
make build                           # Build
make run                             # Run the API service
make test                            # Test
make test-race                       # Test with race detector
make fmt                             # gofmt check (reports; never rewrites)
make check                           # fmt + vet + test — what CI runs
# goose is NOT vendored or installed by the repo. Either install it once —
#   go install github.com/pressly/goose/v3/cmd/goose@latest   (needs ~/go/bin on PATH)
# — or run it without installing, which is what the commands below assume:
GOOSE="go run github.com/pressly/goose/v3/cmd/goose@latest"
export DATABASE_URL="postgres://groundwork:groundwork@localhost:5432/groundwork?sslmode=disable"

$GOOSE -dir migrations postgres "$DATABASE_URL" up      # Apply migrations
$GOOSE -dir migrations postgres "$DATABASE_URL" down    # Roll back one
$GOOSE -dir migrations postgres "$DATABASE_URL" status  # What is applied
$GOOSE -dir migrations create <name> sql                # New migration
```

**Windows prerequisites** (Linux/macOS need none of this — they use the bundled static libraries,
as do CI and production):

```bash
# 1. C toolchain
winget install -e --id MSYS2.MSYS2
C:/msys64/usr/bin/pacman.exe -S --needed mingw-w64-ucrt-x86_64-gcc
# Add C:\msys64\ucrt64\bin to your user PATH, then:
go env -w CGO_ENABLED=1

# 2. DuckDB C library — the OFFICIAL release, version-matched to the driver
DD="$USERPROFILE/.local/duckdb/1.5.5"; mkdir -p "$DD"
curl -sSL -o /tmp/libduckdb.zip \
  https://github.com/duckdb/duckdb/releases/download/v1.5.5/libduckdb-windows-amd64.zip
unzip -o -q /tmp/libduckdb.zip -d "$DD"
cp "$DD/duckdb.dll" "$DD/libduckdb.dll"   # `-lduckdb` resolves against the DLL directly

# 3. Verify
cd server && make doctor
```

**Do NOT install `mingw-w64-ucrt-x86_64-duckdb`, and remove it if present**
(`pacman -R mingw-w64-ucrt-x86_64-duckdb`). It is version-behind (1.4.4 vs the 1.5.5 the driver
targets) and installs into the compiler's *default* search path, so the driver's own
`#cgo LDFLAGS: -lduckdb` finds it even with no `-L` pointing there. The binary then imports **both**
DuckDB runtimes and binds symbols from whichever the loader reaches first — it links, it runs, and
`SELECT version()` quietly reports the wrong one. `make doctor` checks for this explicitly.

Installing gcc alone is not enough — `CGO_ENABLED` is cached in `go env` and stays `0`, so builds
keep failing with "C source files not allowed when not using cgo" while gcc sits on PATH.

If a Windows link fails with `undefined reference to __emutls_v._ZSt11__once_call`, you invoked
`go` directly instead of `make`. That symbol is an ABI mismatch, not a missing package: the
bundled static libs were built by a **win32-threads** GCC (emulated TLS), while MSYS2's GCC is
`--enable-threads=posix --enable-tls` and its libstdc++ defines no such symbol. No additional
install fixes it; the `duckdb_use_lib` linkage in the Makefile is the fix.

**Web (`web/`)**
```bash
npm install
cp .env.example .env.local           # then: npx auth secret  → AUTH_SECRET
npm run dev                          # Dev server
npm run build                        # Production build (needs AUTH_SECRET set)
npm run test                         # Vitest
npm run lint                         # ESLint
npm run typecheck                    # tsc --noEmit
npx next typegen                     # Regenerate PageProps/LayoutProps after adding a route
```

**This Next.js version is not the one in your training data.** `web/AGENTS.md` is written by
`next dev` and says so. Before writing app code, read the bundled docs at
`web/node_modules/next/dist/docs/`. Changes already hit in Foundation:

- `middleware.ts` → **`proxy.ts`**. A file named `middleware.ts` is deprecated and will not run.
- `params` and `searchParams` are **Promises** and must be awaited; synchronous access was removed.
- Route prop types come from `next typegen` as global `PageProps<'/route'>` / `LayoutProps<'/'>`.
- Parallel-route slots now require an explicit `default.js`, or the build fails.
