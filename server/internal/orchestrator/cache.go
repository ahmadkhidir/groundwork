package orchestrator

import (
	"context"
	"errors"
)

// ErrCacheMiss is returned by CacheStore.Get when no cached answer matches the key. It is distinct
// from a storage failure so the run executor can treat a miss as "run it normally" and a real error
// as "log and run it normally anyway" (caching is best-effort accelerator, never a correctness gate).
var ErrCacheMiss = errors.New("cache miss")

// CacheRecord is the persisted output of one fully completed run, keyed for reuse (FEAT-027).
//
// Determinism caching rests on the idea that the SAME question against the SAME schema (and, for an
// edited-SQL run, the SAME seed SQL) deterministically produces the SAME execute/chart/narrate
// outputs — so a later identical run can replay those persisted outputs instead of re-asking the
// model and re-running the engine. The reconcile summary is stored with them so the replayed report
// reports the same rows_in/rows_out honestly, never a fabricated one.
//
// Pass-1 boundary (recorded in the wave report): the key gates on SCHEMA (columns/types/metrics/
// relationships), not on the underlying DATA rows. A replaced Parquet file with identical shape but
// different contents therefore still matches the cache. The viewer always surfaces a cache-hit, so
// a replayed answer is never mistaken for freshly computed; a data-version stamp is the recommended
// Phase-2 hardening.
type CacheRecord struct {
	WorkspaceID string
	// QuestionHash is sha256 of the whitespace-normalized question text.
	QuestionHash string
	// SchemaHash is sha256 of a canonical serialization of the workspace's queryable schema.
	SchemaHash string
	// SeedSQL is the user's edited SQL for a seeded run, or "" for a normal question run. It is part
	// of the cache key, so an edited-SQL run never shadows the original question's answer.
	SeedSQL string
	// SourceRunID is the id of the run whose outputs were cached — surfaced to the user as the
	// "cached from run" reference (the cache.hit event's CachedRunID).
	SourceRunID string

	// PlanOutput is "" for a seeded run (no plan step); otherwise the plan step's JSON output.
	PlanOutput string
	// GenerateSQLOutput is the generate_sql step's JSON output.
	GenerateSQLOutput string
	// ExecuteOutput is the execute step's JSON output (the facts).
	ExecuteOutput string
	// ChartSpecOutput is the chart_spec step's JSON output.
	ChartSpecOutput string
	// NarrateOutput is the narrate step's JSON output.
	NarrateOutput string
	// ReconciliationJSON is the run's domain.ReconciliationSummary, JSON-encoded, so the replayed
	// report reports the same measured rows_in/rows_out as the source run.
	ReconciliationJSON string
}

// CacheStore persists and reads CacheRecords. Satisfied by postgres.RunCacheRepository (no cache is
// wired when it is nil, which disables FEAT-027 without otherwise changing run behavior).
type CacheStore interface {
	// Get returns the cached record for the exact key, or ErrCacheMiss when none exists.
	Get(ctx context.Context, workspaceID, questionHash, schemaHash, seedSQL string) (CacheRecord, error)
	// Put stores record keyed by its workspace + hashes + seed. Implementations upsert on key
	// collision so a deterministically-identical later run overwrites rather than duplicates.
	Put(ctx context.Context, record CacheRecord) error
}
