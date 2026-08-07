package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrProvenanceLinkNotFound is returned when no provenance link matches the given id within the
// workspace.
var ErrProvenanceLinkNotFound = fmt.Errorf("provenance link not found")

// ProvenanceRepository persists and reads claim-level provenance links (FEAT-030). The links live in
// their own table (migration 00001) because they are read by the provenance-panel detail endpoint
// independently of the report row; the Report row itself carries only the link ids.
type ProvenanceRepository struct {
	pool *Pool
}

// NewProvenanceRepository constructs a ProvenanceRepository backed by pool.
func NewProvenanceRepository(pool *Pool) *ProvenanceRepository {
	return &ProvenanceRepository{pool: pool}
}

// WriteLinks inserts every link in links. It is called from the executor's report-materialization
// path (run.Executor.createReport) with each link's ReportID already set, so the report-scoped
// foreign key is always populated. The method is idempotent per link id (a duplicate insert fails
// loudly, which is the executor's corrupt-state signal).
func (r *ProvenanceRepository) WriteLinks(ctx context.Context, workspaceID string, links []domain.ProvenanceLink) error {
	for _, link := range links {
		_, err := r.pool.Exec(ctx,
			`INSERT INTO provenance_links
			   (id, report_id, workspace_id, claim_text, stated_value, source_query, source_column, source_row_ids)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			link.ID, link.ReportID, workspaceID, link.ClaimText, link.StatedValue,
			link.SourceQuery, link.SourceColumn, link.SourceRowIDs,
		)
		if err != nil {
			return fmt.Errorf("inserting provenance link %s: %w", link.ID, err)
		}
	}
	return nil
}

// ListByReport returns the report's links in stable (claim_text) order, for the report read endpoint
// to expand the report's provenance_links id array into full link objects.
func (r *ProvenanceRepository) ListByReport(ctx context.Context, reportID string) ([]domain.ProvenanceLink, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, report_id, claim_text, stated_value, source_query, source_column, source_row_ids
		   FROM provenance_links
		  WHERE report_id = $1
		  ORDER BY claim_text`,
		reportID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing provenance links: %w", err)
	}
	defer rows.Close()
	return scanProvenanceLinks(rows)
}

// GetByID returns a single link scoped to the workspace. WorkspaceID is required so a link id can
// never be enumerated across workspaces — the same ownership discipline as every other read.
func (r *ProvenanceRepository) GetByID(ctx context.Context, workspaceID, id string) (domain.ProvenanceLink, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, report_id, claim_text, stated_value, source_query, source_column, source_row_ids
		   FROM provenance_links
		  WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	)
	link, err := scanProvenanceLink(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return domain.ProvenanceLink{}, ErrProvenanceLinkNotFound
		}
		return domain.ProvenanceLink{}, fmt.Errorf("loading provenance link %s: %w", id, err)
	}
	return link, nil
}

// scanProvenanceLink scans one provenance_links row.
func scanProvenanceLink(row pgx.Row) (domain.ProvenanceLink, error) {
	var link domain.ProvenanceLink
	err := row.Scan(
		&link.ID,
		&link.ReportID,
		&link.ClaimText,
		&link.StatedValue,
		&link.SourceQuery,
		&link.SourceColumn,
		&link.SourceRowIDs,
	)
	return link, err
}

// scanProvenanceLinks scans every row of a provenance_links query sharing the column list above.
func scanProvenanceLinks(rows pgx.Rows) ([]domain.ProvenanceLink, error) {
	var out []domain.ProvenanceLink
	for rows.Next() {
		var link domain.ProvenanceLink
		if err := rows.Scan(
			&link.ID,
			&link.ReportID,
			&link.ClaimText,
			&link.StatedValue,
			&link.SourceQuery,
			&link.SourceColumn,
			&link.SourceRowIDs,
		); err != nil {
			return nil, fmt.Errorf("scanning provenance link: %w", err)
		}
		out = append(out, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating provenance links: %w", err)
	}
	return out, nil
}
