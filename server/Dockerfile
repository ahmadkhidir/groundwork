# syntax=docker/dockerfile:1

# Groundwork API service image (Fly.io app "groundwork-api").
#
# Build constraints that MUST never regress (IMPLEMENTATION.md):
#  - R-010: the DuckDB Go driver needs cgo. Linux links the static libraries bundled in
#    duckdb-go-bindings (no build tags), so CGO_ENABLED=1 is the only requirement here.
#  - R-017: the production container must NEVER `INSTALL` DuckDB extensions at runtime from
#    extensions.duckdb.org (locked-down egress would fail the first Excel/SQLite upload). The
#    `excel` and `sqlite_scanner` extensions are installed at BUILD time into the same
#    ~/.duckdb/extensions/<version>/<platform> path the driver reads at runtime, pinned to the
#    exact DuckDB version the driver targets (1.5.5 — keep in step with server/go.mod).

# ---- build stage: compile the API with cgo against the bundled DuckDB static libs -------------
FROM golang:1.26-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# ---- extensions stage: preinstall the DuckDB excel + sqlite_scanner extensions (R-017) ---------
FROM debian:bookworm-slim AS extensions
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates unzip \
    && rm -rf /var/lib/apt/lists/*

ARG DUCKDB_VERSION=1.5.5
WORKDIR /ext
ENV HOME=/ext
# The official CLI, version-matched to the driver, installs the extensions into $HOME/.duckdb in
# the exact layout the Go driver's extension auto-loader uses at runtime. LOAD verifies each
# extension actually works before the CLI is discarded.
RUN curl -fsSL -o duckdb_cli.zip "https://github.com/duckdb/duckdb/releases/download/v${DUCKDB_VERSION}/duckdb_cli-linux-amd64.zip" \
    && unzip -q duckdb_cli.zip \
    && rm duckdb_cli.zip \
    && chmod +x duckdb \
    && ./duckdb -c "INSTALL excel;" \
    && ./duckdb -c "INSTALL sqlite_scanner;" \
    && ./duckdb -c "LOAD excel; LOAD sqlite_scanner; SELECT 1;" \
    && rm duckdb

# ---- runtime stage -----------------------------------------------------------------------------
FROM debian:bookworm-slim AS runtime
# ca-certificates is mandatory: the API talks HTTPS to Neon, R2, Anthropic, and Stripe.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 app

WORKDIR /app
# HOME must match where the preinstalled extensions were copied — DuckDB resolves its extension
# directory against $HOME/.duckdb.
ENV HOME=/home/app
ENV ENVIRONMENT=production

COPY --from=build /out/api /app/api
COPY --from=extensions /ext/.duckdb /home/app/.duckdb

USER app
EXPOSE 8080
CMD ["/app/api"]
