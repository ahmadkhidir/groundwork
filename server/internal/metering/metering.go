// Package metering is the usage-metering seam (Billing milestone, pass 1): the infrastructure that
// makes usage_records actually get written from the three real metering sources the product
// documents — engine-seconds (engine releases), object-store bytes (ingest completion, dataset
// deletion, and a daily reconciliation sweep), and model tokens (model calls) — so FEAT-047's usage
// view shows real numbers and the later Stripe passes have data to bill against.
//
// Metering is recorded per workspace, from day one, regardless of billing status (architecture.md's
// Logging & Monitoring section; monetization.md R-015's mitigation). All writes are BEST-EFFORT: a
// metering failure must never fail a model call, an engine release, or an ingest. Metering is
// accounting — tunable cost data for the idle-teardown threshold and the billing allotments — not
// evidence; the fail-closed contract belongs to the audit/egress log, which is written in the same
// code path as the action it records. Metering callers log and swallow.
//
// Period discipline: every source records into the same period — the UTC calendar month containing
// the event, computed by PeriodFor — so the read side (FEAT-047's "current period" is the most
// recent record, per api/usage.go) and later billing agree on what "the current period" means
// without each source re-deriving the rule.
//
// This package is a leaf: it imports only the standard library and depends on narrow interfaces
// (UsageUpserter, StorageSizer) so it can be tested without a database or object store. The
// implementations live elsewhere — postgres.UsageRepository satisfies UsageUpserter, and
// storage.Client satisfies StorageSizer once the orchestrator lands SumBytesByPrefix.
package metering

import (
	"context"
	"fmt"
	"time"
)

// PeriodFor returns the UTC calendar-month period containing t: period_start is the first instant
// of t's UTC month and period_end is the first instant of the FOLLOWING month (exclusive), so the
// half-open interval [period_start, period_end) covers exactly one calendar month.
//
// The period is a UTC calendar month — the natural billing month for monetization.md's "monthly
// allotment" language, and the period the read side and later billing expect. Every metering
// source records through this helper so all of them land in the same period by construction.
// Year rollover and leap months fall out of time.Date/AddDate: December 2026 ends at
// 2027-01-01T00:00:00Z, and February always gets its correct length.
func PeriodFor(t time.Time) (start, end time.Time) {
	utc := t.UTC()
	start = time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
	end = start.AddDate(0, 1, 0)
	return start, end
}

// EngineSeconds returns the fractional seconds between the lease start (engine.Handle.AcquiredAt)
// and the release, the unit engine-seconds metering records.
//
// A non-positive elapsed duration — a zero-length lease or clock skew between the process reading
// AcquiredAt and now — reads as 0 rather than a negative meter, which would silently reduce a
// workspace's billed engine-seconds instead of honestly reporting "too short to meter".
func EngineSeconds(acquiredAt, releasedAt time.Time) float64 {
	elapsed := releasedAt.Sub(acquiredAt)
	if elapsed <= 0 {
		return 0
	}
	return elapsed.Seconds()
}

// WorkspacePrefix returns the object-storage key prefix that holds every object for workspaceID.
//
// It is the same "workspaces/{id}/" convention storage.DatasetKey and storage.NormalizedDatasetKey
// build on (their keys are workspaces/{id}/datasets/{ds}/...), so summing under this prefix counts
// exactly the workspace's original uploads and normalized Parquet files. The literal is kept here
// rather than imported from storage so this package stays a leaf; if the key convention ever moves,
// both places change together (documented coupling).
func WorkspacePrefix(workspaceID string) string {
	return "workspaces/" + workspaceID + "/"
}

// UsageWriter is the seam every metering source calls. The accumulate-vs-replace contract is
// encoded in the method split:
//
//   - Record ACCUMULATES: engine-seconds add across engine runs and model tokens add across model
//     calls, so the current period's totals are the sum of every event. Callers pass 0 for the
//     metric they are not reporting; the underlying upsert adds 0 harmlessly.
//   - SetStorageBytes REPLACES: storage_bytes is a point-in-time snapshot ("as of period_end" per
//     schema.md), so a storage event supersedes the period's earlier snapshot rather than adding to
//     it.
//
// Satisfied by the writer NewUsageWriter returns, which delegates to a period-parameterized
// UsageUpserter (postgres.UsageRepository). Best-effort by contract: callers log and swallow, and
// a metering failure must never fail the model call, engine release, or ingest that produced the
// event.
type UsageWriter interface {
	// Record accumulates engineSeconds and modelTokens into the workspace's current UTC-month
	// period, creating the period's record when none exists yet.
	Record(ctx context.Context, workspaceID string, engineSeconds float64, modelTokens int64) error
	// SetStorageBytes replaces the workspace's current UTC-month period's storage_bytes snapshot
	// with bytes, creating the period's record when none exists yet.
	SetStorageBytes(ctx context.Context, workspaceID string, bytes int64) error
}

// UsageUpserter is the period-parameterized write side of usage_records, satisfied by
// postgres.UsageRepository (its Upsert and SetStorageBytes methods). Period computation lives in
// this package (via PeriodFor) rather than in the store so every source and the reconciliation
// record into the same period by construction; the store stays a dumb, period-agnostic SQL layer.
type UsageUpserter interface {
	// Upsert accumulates engineSeconds and modelTokens into the (workspace_id, period_start)
	// record identified by the explicit period, creating it when absent. The conflict path updates
	// ONLY the accumulating columns (storage_bytes is untouched).
	Upsert(ctx context.Context, workspaceID string, periodStart, periodEnd time.Time, engineSeconds float64, modelTokens int64) error
	// SetStorageBytes replaces the (workspace_id, period_start) record's storage_bytes with bytes,
	// creating the record when absent. The conflict path updates ONLY storage_bytes (the
	// accumulating columns are untouched).
	SetStorageBytes(ctx context.Context, workspaceID string, periodStart, periodEnd time.Time, bytes int64) error
}

// StorageSizer sums the object sizes under a key prefix, satisfied by storage.Client once the
// orchestrator lands SumBytesByPrefix (the DeleteObjectsByPrefix-style read the reconciliation
// needs).
type StorageSizer interface {
	SumBytesByPrefix(ctx context.Context, prefix string) (int64, error)
}

// Reconciler refreshes a workspace's storage_bytes snapshot — the narrow seam the ingest pipeline
// and dataset-delete handler call when a workspace's object set changes. Best-effort by contract:
// the caller logs and swallows, exactly like every other metering seam.
type Reconciler interface {
	Reconcile(ctx context.Context, workspaceID string) error
}

// writer adapts a UsageUpserter to the UsageWriter seam by computing the current UTC-month period
// for every call. now is injectable so tests can pin the period; it defaults to time.Now.
type writer struct {
	repo UsageUpserter
	now  func() time.Time
}

// NewUsageWriter returns a UsageWriter recording into the current UTC calendar month. repo is
// typically postgres.UsageRepository; the returned writer satisfies UsageWriter and is what the
// engine hook, model client, and reconciliation loop should call.
func NewUsageWriter(repo UsageUpserter) UsageWriter {
	return &writer{repo: repo, now: time.Now}
}

// Record implements UsageWriter (accumulate).
func (w *writer) Record(ctx context.Context, workspaceID string, engineSeconds float64, modelTokens int64) error {
	start, end := PeriodFor(w.now())
	return w.repo.Upsert(ctx, workspaceID, start, end, engineSeconds, modelTokens)
}

// SetStorageBytes implements UsageWriter (replace).
func (w *writer) SetStorageBytes(ctx context.Context, workspaceID string, bytes int64) error {
	start, end := PeriodFor(w.now())
	return w.repo.SetStorageBytes(ctx, workspaceID, start, end, bytes)
}

// storageReconciler adapts a StorageSizer and a UsageWriter to Reconciler.
type storageReconciler struct {
	objects StorageSizer
	writer  UsageWriter
}

// NewStorageReconciler returns a Reconciler that sums workspaceID's object bytes and replaces the
// current period's storage_bytes snapshot. One instance serves both event sites (ingest
// completion, dataset deletion) and the daily sweep in main.go.
func NewStorageReconciler(objects StorageSizer, writer UsageWriter) Reconciler {
	return &storageReconciler{objects: objects, writer: writer}
}

// Reconcile implements Reconciler.
func (r *storageReconciler) Reconcile(ctx context.Context, workspaceID string) error {
	return ReconcileStorage(ctx, workspaceID, r.objects, r.writer)
}

// ReconcileStorage sums workspaceID's object-store bytes and replaces the current period's
// storage_bytes snapshot — the daily reconciliation's unit of work, and the same operation the
// ingest-completion and dataset-delete event sites run so the snapshot stays current between
// sweeps.
//
// Idempotent: running it twice for the same period writes the same snapshot (the second upsert
// replaces the first), so the daily sweep and an event-driven reconcile can never double-count.
// An empty workspace is a no-op-safe success: a prefix with no objects sums to 0 and the snapshot
// is written as 0 — truthful "as of period_end" data per schema.md, and the repair mechanism for a
// workspace whose objects were removed without a delete event reaching the metering seam.
//
// The returned error is for the CALLER to handle; metering seams are best-effort, so event sites
// log and swallow while the daily sweep logs and continues to the next workspace.
func ReconcileStorage(ctx context.Context, workspaceID string, objects StorageSizer, writer UsageWriter) error {
	total, err := objects.SumBytesByPrefix(ctx, WorkspacePrefix(workspaceID))
	if err != nil {
		return fmt.Errorf("metering: summing storage for workspace %s: %w", workspaceID, err)
	}
	if err := writer.SetStorageBytes(ctx, workspaceID, total); err != nil {
		return fmt.Errorf("metering: recording storage snapshot for workspace %s: %w", workspaceID, err)
	}
	return nil
}
