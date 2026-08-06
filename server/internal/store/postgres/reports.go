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
	var (
		report         domain.Report
		chartSpecJSON  []byte
		reconJSON      []byte
		parentReportID *string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, workspace_id, run_id, title, narrative, chart_spec, reconciliation_summary,
		        parent_report_id, created_at
		 FROM reports WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	).Scan(&report.ID, &report.WorkspaceID, &report.RunID, &report.Title, &report.Narrative,
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

	// FEAT-030 (Phase 3) populates provenance_links; this wave's reports always carry an empty
	// array. A nil slice would serialize as JSON null, and schema.md requires an array — so always
	// materialize an empty one rather than ever returning null.
	report.ProvenanceLinks = []string{}
	return report, nil
}
