# Groundwork

An AI analytics platform where every number in an answer traces back to a query result.

## The problem

Ask a language model a question about your data and it will answer confidently, in prose, with
numbers it may have invented. Groundwork removes the room for invention. The model writes SQL, a
disposable engine executes it, and the model narrates only over the rows that came back.

## How it works

Upload a CSV, Excel, Parquet, or SQLite file. You get chart-illustrated answers, and each claim is
bound to the cells that produced it.

The answer loop is two stages:

1. **The model writes SQL** against the workspace schema.
2. **DuckDB executes it** — a throwaway engine per run, not a shared warehouse session.
3. **The model narrates over the returned facts**, never over the data itself.

On top of that sit four verification layers:

- a determinism cache, so identical questions do not produce drifting answers
- static analysis of generated SQL before execution
- post-execution reconciliation between the question and the result shape
- claim-level provenance binding each number in the narrative to its backing cells

## Stack

| Layer | Choice |
|---|---|
| Backend | Go 1.26, chi, pgx, goose |
| Orchestration | Durable run/step DAG in Postgres, with leases, heartbeats, and LISTEN/NOTIFY |
| Query engine | DuckDB, one disposable process per run |
| Frontend | Next.js 16 (App Router), React 19, Tailwind v4, first-party SVG charts |
| Auth | Auth.js (next-auth v5), JWE cookie sessions |
| Model | Anthropic Claude |
| Local infra | Postgres 18 and MinIO via docker-compose (standing in for Neon and Cloudflare R2) |

## Structure

```
server/     Go service: cmd, internal, migrations
web/        Next.js app
docs/       product specification (git submodule)
```

## Running locally

```bash
docker compose up -d            # Postgres + MinIO

cd server
make doctor                     # check the toolchain first
make build
make run
make test
```

Migrations:

```bash
GOOSE="go run github.com/pressly/goose/v3/cmd/goose@latest"
export DATABASE_URL="postgres://groundwork:groundwork@localhost:5432/groundwork?sslmode=disable"
$GOOSE -dir migrations postgres "$DATABASE_URL" up
```

## A note on the build

DuckDB requires cgo, and the linkage differs between Windows and Linux. Use the `server/Makefile`
targets rather than bare `go build`; a direct build on Windows fails at link time with an error that
looks like a compiler bug.

## Design decisions

**The engine is disposable.** Each run gets a fresh DuckDB process. A malformed or expensive query
takes down one run, not the workspace.

**The DAG is durable.** Runs survive a restart, because an analytics run that has to be repeated
from the beginning is a worse product than one that resumes.

**Privacy is enforced at the boundary.** A shared payload builder strips PII before any model call,
egress is logged fail-closed, and deleting a workspace deletes its contents in one cascade.

## License

MIT. See [LICENSE](LICENSE).
