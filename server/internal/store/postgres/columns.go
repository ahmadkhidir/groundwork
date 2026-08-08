package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrColumnNotFound is returned when no column matches the given id.
var ErrColumnNotFound = errors.New("column not found")

// ColumnRepository persists and retrieves profiled columns (FEAT-005), keyed to their owning
// Dataset. Written by the Ingest Pipeline once profiling.Profile computes statistics, and read back
// by the ingest confirmation flow (FEAT-006/007/009/010) and relationship inference (FEAT-008) —
// "profiling statistics are stored and retrievable, not recomputed on every use" is a features.md
// acceptance criterion, not an implementation detail.
type ColumnRepository struct {
	pool *Pool
}

// NewColumnRepository constructs a ColumnRepository backed by pool.
func NewColumnRepository(pool *Pool) *ColumnRepository {
	return &ColumnRepository{pool: pool}
}

// BulkInsert persists every column in cols in a single round trip, scoped to workspaceID.
//
// workspaceID is a separate parameter rather than a domain.Column field: schema.md's Column entity
// (mirrored exactly by domain.Column, which is read-only shared ground truth) has no workspace_id
// property — only dataset_id. The database column exists purely as a denormalization (see migration
// 00001's comment on columns.workspace_id) so a workspace-scoped query never needs to join through
// datasets; it is a storage-layer detail, not part of the domain shape.
func (r *ColumnRepository) BulkInsert(ctx context.Context, workspaceID string, cols []domain.Column) error {
	if len(cols) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, c := range cols {
		batch.Queue(
			`INSERT INTO columns
			   (id, dataset_id, workspace_id, name, inferred_type, type_confidence, type_fit_rates,
			    pii_flag, pii_pattern, pii_reviewed_at, type_overridden_at,
			    excluded_from_model, null_rate, distinct_count)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			c.ID, c.DatasetID, workspaceID, c.Name, string(c.InferredType), c.TypeConfidence, fitRatesJSON(c.TypeFitRates),
			c.PIIFlag, c.PIIPattern, nullableTime(c.PIIReviewedAt), nullableTime(c.TypeOverriddenAt),
			c.ExcludedFromModel, c.NullRate, c.DistinctCount,
		)
	}

	br := r.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range cols {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("inserting column: %w", err)
		}
	}
	return nil
}

// ListByDataset returns every column belonging to datasetID, in stable name order.
func (r *ColumnRepository) ListByDataset(ctx context.Context, datasetID string) ([]domain.Column, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, dataset_id, name, inferred_type, type_confidence, type_fit_rates,
		        pii_flag, pii_pattern, pii_reviewed_at, type_overridden_at,
		        excluded_from_model, null_rate, distinct_count
		 FROM columns WHERE dataset_id = $1 ORDER BY name ASC`,
		datasetID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing columns: %w", err)
	}
	defer rows.Close()
	return scanColumns(rows)
}

// ListByWorkspace returns every column across every dataset in workspaceID — used by relationship
// inference, which compares columns across datasets within the same workspace.
func (r *ColumnRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Column, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, dataset_id, name, inferred_type, type_confidence, type_fit_rates,
		        pii_flag, pii_pattern, pii_reviewed_at, type_overridden_at,
		        excluded_from_model, null_rate, distinct_count
		 FROM columns WHERE workspace_id = $1 ORDER BY dataset_id, name ASC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing workspace columns: %w", err)
	}
	defer rows.Close()
	return scanColumns(rows)
}

// scanColumns scans every row of a `columns` query sharing the five-statistic column list above.
func scanColumns(rows pgx.Rows) ([]domain.Column, error) {
	var out []domain.Column
	for rows.Next() {
		var (
			c                domain.Column
			inferredType     string
			fitRates         []byte
			piiReviewedAt    *time.Time
			typeOverriddenAt *time.Time
		)
		if err := rows.Scan(&c.ID, &c.DatasetID, &c.Name, &inferredType, &c.TypeConfidence, &fitRates,
			&c.PIIFlag, &c.PIIPattern, &piiReviewedAt, &typeOverriddenAt,
			&c.ExcludedFromModel, &c.NullRate, &c.DistinctCount); err != nil {
			return nil, fmt.Errorf("scanning column: %w", err)
		}
		c.InferredType = domain.InferredType(inferredType)
		c.TypeFitRates = parseFitRates(fitRates)
		c.PIIReviewedAt = formatNullableTime(piiReviewedAt)
		c.TypeOverriddenAt = formatNullableTime(typeOverriddenAt)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating columns: %w", err)
	}
	return out, nil
}

// GetByID returns the column with id, or ErrColumnNotFound.
func (r *ColumnRepository) GetByID(ctx context.Context, id string) (domain.Column, error) {
	var (
		c                domain.Column
		inferredType     string
		fitRates         []byte
		piiReviewedAt    *time.Time
		typeOverriddenAt *time.Time
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, dataset_id, name, inferred_type, type_confidence, type_fit_rates,
		        pii_flag, pii_pattern, pii_reviewed_at, type_overridden_at,
		        excluded_from_model, null_rate, distinct_count
		 FROM columns WHERE id = $1`,
		id,
	).Scan(&c.ID, &c.DatasetID, &c.Name, &inferredType, &c.TypeConfidence, &fitRates,
		&c.PIIFlag, &c.PIIPattern, &piiReviewedAt, &typeOverriddenAt,
		&c.ExcludedFromModel, &c.NullRate, &c.DistinctCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Column{}, ErrColumnNotFound
		}
		return domain.Column{}, fmt.Errorf("getting column: %w", err)
	}
	c.InferredType = domain.InferredType(inferredType)
	c.TypeFitRates = parseFitRates(fitRates)
	c.PIIReviewedAt = formatNullableTime(piiReviewedAt)
	c.TypeOverriddenAt = formatNullableTime(typeOverriddenAt)
	return c, nil
}

// GetByIDForWorkspace returns the column with id, scoped to workspaceID, or ErrColumnNotFound if it
// doesn't exist or belongs to a different workspace. The same "not found" response covers both
// cases — screens/07-ingest-confirmation.md's review/override endpoints (FEAT-006/FEAT-007) must
// never confirm that a column id exists in some OTHER workspace the caller doesn't own, matching this
// API's existing non-enumeration pattern (see handleListColumns's doc comment).
func (r *ColumnRepository) GetByIDForWorkspace(ctx context.Context, id, workspaceID string) (domain.Column, error) {
	var (
		c                domain.Column
		inferredType     string
		fitRates         []byte
		piiReviewedAt    *time.Time
		typeOverriddenAt *time.Time
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, dataset_id, name, inferred_type, type_confidence, type_fit_rates,
		        pii_flag, pii_pattern, pii_reviewed_at, type_overridden_at,
		        excluded_from_model, null_rate, distinct_count
		 FROM columns WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	).Scan(&c.ID, &c.DatasetID, &c.Name, &inferredType, &c.TypeConfidence, &fitRates,
		&c.PIIFlag, &c.PIIPattern, &piiReviewedAt, &typeOverriddenAt,
		&c.ExcludedFromModel, &c.NullRate, &c.DistinctCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Column{}, ErrColumnNotFound
		}
		return domain.Column{}, fmt.Errorf("getting column for workspace: %w", err)
	}
	c.InferredType = domain.InferredType(inferredType)
	c.TypeFitRates = parseFitRates(fitRates)
	c.PIIReviewedAt = formatNullableTime(piiReviewedAt)
	c.TypeOverriddenAt = formatNullableTime(typeOverriddenAt)
	return c, nil
}

// UpdateInferredType overrides a column's inferred type per FEAT-006 — the user's explicit review
// decision on screens/07-ingest-confirmation.md — and moves type_confidence to the fit rate measured
// for the newly-chosen type.
//
// This previously set type_confidence to a flat 1.0, on the reasoning that a human-confirmed value
// outranks a heuristic guess. That reasoning does not survive contact with what the number is for. A
// user correcting `text` to `whole number` on a column where a third of the values cannot convert was
// shown HIGH / 100% — not stale, but fabricated, and precisely on the screen README calls the
// product's highest-risk UX. Confirming a type says what the user INTENDED; it says nothing about
// whether the data supports it, and only the second of those is worth a badge.
//
// The rate is read from the fit rates measured once at profiling, so this is a lookup inside one
// UPDATE: no engine query, and no waking a sleeping engine to redraw a badge (FEAT-011).
//
// COALESCE handles columns profiled before migration 00006, whose type_fit_rates is '{}': those keep
// their existing type_confidence rather than dropping to a confident-looking zero. Such a column
// reports the old inference's rate until it is re-ingested — visibly imperfect, but honest, and it
// cannot be fixed without re-scanning data this function deliberately does not touch.
func (r *ColumnRepository) UpdateInferredType(ctx context.Context, id string, t domain.InferredType) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE columns
		    SET inferred_type = $2,
		        type_confidence = COALESCE((type_fit_rates ->> $2)::double precision, type_confidence),
		        type_overridden_at = now()
		  WHERE id = $1`,
		id, string(t),
	)
	if err != nil {
		return fmt.Errorf("updating column inferred type: %w", err)
	}
	return nil
}

// fitRatesJSON encodes a column's per-type fit rates for storage. A nil or empty map is stored as an
// empty JSON object rather than NULL, matching the column's NOT NULL DEFAULT '{}'.
func fitRatesJSON(rates map[domain.InferredType]float64) []byte {
	if len(rates) == 0 {
		return []byte("{}")
	}
	out := make(map[string]float64, len(rates))
	for t, rate := range rates {
		out[string(t)] = rate
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		// Only unencodable floats (NaN/Inf) can reach here, which would mean a profiling bug. Storing
		// an empty object degrades to the pre-00006 behaviour rather than failing the whole ingest
		// over a badge.
		return []byte("{}")
	}
	return encoded
}

// parseFitRates decodes stored per-type fit rates. Unparseable or absent data yields a nil map, which
// callers must read as "unknown" — never as zero confidence in every type.
func parseFitRates(raw []byte) map[domain.InferredType]float64 {
	if len(raw) == 0 {
		return nil
	}
	var decoded map[string]float64
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded) == 0 {
		return nil
	}
	out := make(map[domain.InferredType]float64, len(decoded))
	for name, rate := range decoded {
		out[domain.InferredType(name)] = rate
	}
	return out
}

// UpdateExclusion overrides a column's excluded_from_model flag per FEAT-010 — the user's explicit
// decision, on Workspace Settings' Column Visibility tab (screens/13), that this column must never
// leave the trust boundary to the model.
//
// Exclusion governs EGRESS, not access: the column remains queryable by the engine (usable in a
// WHERE clause, a join, an aggregate), only its name/values are withheld from every model payload by
// the shared payload-construction seam (model.BuildPayload / BuildFactsPayload, which filter on
// pii_flag OR excluded_from_model). That separation is exactly why the flag is a boolean write here
// rather than a schema change: the query path never reads it.
func (r *ColumnRepository) UpdateExclusion(ctx context.Context, id string, excluded bool) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE columns SET excluded_from_model = $2 WHERE id = $1`,
		id, excluded,
	)
	if err != nil {
		return fmt.Errorf("updating column exclusion: %w", err)
	}
	return nil
}

// UpdatePII overrides a column's PII flag and detected pattern per FEAT-007 — the user's explicit
// review decision on screens/07-ingest-confirmation.md. pattern is caller-resolved: empty when
// clearing a flag (schema.md's Column.pii_pattern is "empty string if not flagged"), or a specific
// pattern name when setting one — either the pattern DetectAll originally proposed, re-confirmed, or
// "manual" when the user flags a column DetectAll did not.
//
// reviewedAt stamps schema.md's Column.pii_reviewed_at in the same statement as the flag itself.
// That pairing is the point of the field: a separate write could leave a cleared flag with no record
// that a human cleared it, which is precisely the state FEAT-007 requires to be distinguishable from
// "never flagged". It is set on every override, including one that re-confirms an existing flag —
// the timestamp records that a human looked, not that the value changed.
func (r *ColumnRepository) UpdatePII(ctx context.Context, id string, flag bool, pattern string, reviewedAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE columns SET pii_flag = $2, pii_pattern = $3, pii_reviewed_at = $4 WHERE id = $1`,
		id, flag, pattern, reviewedAt,
	)
	if err != nil {
		return fmt.Errorf("updating column pii flag: %w", err)
	}
	return nil
}

// nullableTime converts schema.md's empty-string-means-absent timestamp into the NULL the database
// stores, so an unreviewed column holds NULL rather than a zero time that would read as a real
// (and absurdly old) review decision.
func nullableTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		// An unparseable value can only come from a caller that built it by hand rather than from
		// formatNullableTime; treating it as absent is safer than persisting a fabricated instant.
		return nil
	}
	return &t
}

// formatNullableTime is nullableTime's inverse: NULL becomes "", a real timestamp becomes RFC 3339.
func formatNullableTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
