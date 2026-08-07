package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrReportNotFound is returned when no report matches the given id within the workspace.
var ErrReportNotFound = errors.New("report not found")

// ReportRepository persists and retrieves first-class Report objects (FEAT-014 report
// materialization, this wave; FEAT-030/034/021 consumers come in later phases).
//
// The reports table (migration 00001) holds the report's self-contained payload: chart_spec and
// reconciliation_summary as JSONB (they ARE queried structurally, unlike steps.input), and
// provenance via the separate provenance_links table, which FEAT-030 populates — this wave's runs
// always materialize reports with an empty links array per the confirmed Phase 2 boundary.
type ReportRepository struct {
	pool *Pool
}

// NewReportRepository constructs a ReportRepository backed by pool.
func NewReportRepository(pool *Pool) *ReportRepository {
	return &ReportRepository{pool: pool}
}

// Create inserts a new report row.
//
// provenance_links is deliberately NOT written here: it has its own table (migration 00001) and is
// populated by FEAT-030 (Claim-Level Provenance, Phase 3). The caller passes an empty array for
// now, exactly as the Phase 2 boundary authorizes; when provenance lands it is written through the
// provenance_links repository, never through this method.
func (r *ReportRepository) Create(ctx context.Context, report domain.Report) error {
	chartSpecJSON, err := json.Marshal(report.ChartSpec)
	if err != nil {
		return fmt.Errorf("encoding chart spec: %w", err)
	}
	reconJSON, err := json.Marshal(report.ReconciliationSummary)
	if err != nil {
		return fmt.Errorf("encoding reconciliation summary: %w", err)
	}

	_, err = r.pool.Exec(ctx,
		`INSERT INTO reports
		   (id, workspace_id, run_id, title, narrative, chart_spec, reconciliation_summary,
		    parent_report_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		report.ID, report.WorkspaceID, report.RunID, report.Title, report.Narrative,
		chartSpecJSON, reconJSON, nullableString(report.ParentReportID), report.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting report: %w", err)
	}
	return nil
}

// GetByID returns the report with id scoped to workspaceID, or ErrReportNotFound when no such
// report exists in that workspace.
//
// Workspace scoping lives in the SQL (WHERE id = $1 AND workspace_id = $2), not in the handler: a
// report belonging to a different workspace reads as "not found" at the store, which is exactly the
// non-enumeration contract the API layer promises (the same stance handleGetRun documents for runs).
func (r *ReportRepository) GetByID(ctx context.Context, workspaceID, id string) (domain.Report, error) {
	return r.scanReport(r.pool.QueryRow(ctx,
		`SELECT id, workspace_id, run_id, title, narrative, chart_spec, reconciliation_summary,
		        parent_report_id, created_at
		 FROM reports WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	))
}

// GetByRunID returns the report materialized by runID within workspaceID, or ErrReportNotFound when
// no report exists for that run. Backed by idx_reports_run (migration 00001).
//
// FEAT-018 uses this to attach report_id to the GET run response so the Run Viewer can navigate to
// the Report on completion. Workspace scoping lives in the SQL for the same non-enumeration reason
// as GetByID: a run belongs to one workspace, and its report must never be disclosed to a caller who
// owns only a different workspace.
func (r *ReportRepository) GetByRunID(ctx context.Context, workspaceID, runID string) (domain.Report, error) {
	return r.scanReport(r.pool.QueryRow(ctx,
		`SELECT id, workspace_id, run_id, title, narrative, chart_spec, reconciliation_summary,
		        parent_report_id, created_at
		 FROM reports WHERE run_id = $1 AND workspace_id = $2`,
		runID, workspaceID,
	))
}

// ListByWorkspace returns the workspace's most recent reports, ordered by created_at DESC and
// capped at limit. Backs FEAT-034's Recent Reports panel (screens/04-workspace-home.md) — the
// workspace home shows up to 10. Backed by idx_reports_workspace (migration 00001).
//
// Workspace scoping lives in the SQL (WHERE workspace_id = $1), the same non-enumeration stance as
// GetByID: a caller can only ever list reports from workspaces it already owns, because the handler
// proves ownership before calling and the store never queries across workspace ids.
func (r *ReportRepository) ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]domain.Report, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, workspace_id, run_id, title, narrative, chart_spec, reconciliation_summary,
		        parent_report_id, created_at
		 FROM reports WHERE workspace_id = $1
		 ORDER BY created_at DESC LIMIT $2`,
		workspaceID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("listing reports: %w", err)
	}
	defer rows.Close()

	out := []domain.Report{}
	for rows.Next() {
		report, err := r.scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, report)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating reports: %w", err)
	}
	return out, nil
}

// scanReport decodes the shared report row shape selected by GetByID, GetByRunID, and
// ListByWorkspace. The reports table stores chart_spec and reconciliation_summary as JSONB;
// provenance links live in their own table (FEAT-030), so this wave always materializes an empty
// array rather than a JSON null.
func (r *ReportRepository) scanReport(row pgx.Row) (domain.Report, error) {
	var (
		report         domain.Report
		chartSpecJSON  []byte
		reconJSON      []byte
		parentReportID *string
	)
	err := row.Scan(&report.ID, &report.WorkspaceID, &report.RunID, &report.Title, &report.Narrative,
		&chartSpecJSON, &reconJSON, &parentReportID, &report.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Report{}, ErrReportNotFound
		}
		return domain.Report{}, fmt.Errorf("getting report: %w", err)
	}

	if err := json.Unmarshal(chartSpecJSON, &report.ChartSpec); err != nil {
		return domain.Report{}, fmt.Errorf("decoding report chart spec: %w", err)
	}
	if err := json.Unmarshal(reconJSON, &report.ReconciliationSummary); err != nil {
		return domain.Report{}, fmt.Errorf("decoding report reconciliation summary: %w", err)
	}
	if parentReportID != nil {
		report.ParentReportID = *parentReportID
	}

	report.ProvenanceLinks = []string{}
	return report, nil
}
