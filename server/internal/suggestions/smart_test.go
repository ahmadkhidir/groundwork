package suggestions

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// fakeRows is an in-memory engine.Rows for the profiler test.
type fakeRows struct {
	data [][]any
	idx  int
	err  error
}

func (f *fakeRows) Next() bool {
	if f.idx >= len(f.data) {
		return false
	}
	return true
}
func (f *fakeRows) Scan(dest ...any) error {
	row := f.data[f.idx]
	for i := range dest {
		// A nil row value models SQL NULL: it leaves the destination untouched (NullFloat64 stays
		// invalid, pointer/string destinations stay zero) rather than panicking — the same
		// database/sql semantics Profile depends on for empty aggregates.
		switch d := dest[i].(type) {
		case *string:
			if v, ok := row[i].(string); ok {
				*d = v
			}
		case *int64:
			if v, ok := row[i].(int64); ok {
				*d = v
			}
		case *float64:
			if v, ok := row[i].(float64); ok {
				*d = v
			}
		case *sql.NullFloat64:
			if v, ok := row[i].(float64); ok {
				*d = sql.NullFloat64{Float64: v, Valid: true}
			}
		}
	}
	f.idx++
	return nil
}
func (f *fakeRows) Columns() []string { return []string{"v", "c"} }
func (f *fakeRows) Err() error        { return f.err }
func (f *fakeRows) Close() error      { return nil }

// fakeProfiler is an in-memory engine.Handle-shaped profiler that returns canned rows.
type fakeProfiler struct {
	err error
}

func (f *fakeProfiler) Query(_ context.Context, _ string, _ ...any) (engine.Rows, error) {
	if f.err != nil {
		return nil, f.err
	}
	// The exact queries vary; returning an empty result set is enough to prove the PII column is
	// never profiled (its profile would still appear in the output length otherwise).
	return &fakeRows{}, nil
}

func TestProfileSkipsUnsafeColumnsAndBuildsTypeAwareProfiles(t *testing.T) {
	h := &fakeProfiler{}
	datasets := []domain.Dataset{{ID: "ds-1", OriginalFilename: "customers.csv", IngestStatus: domain.IngestComplete}}
	columns := []domain.Column{
		{ID: "c-1", DatasetID: "ds-1", Name: "region", InferredType: domain.TypeString},
		{ID: "c-2", DatasetID: "ds-1", Name: "total_amount", InferredType: domain.TypeNumber},
		{ID: "c-3", DatasetID: "ds-1", Name: "email", InferredType: domain.TypeString, PIIFlag: true},
		{ID: "c-4", DatasetID: "ds-1", Name: "session_time", InferredType: domain.TypeTime},
	}

	profiles, err := Profile(context.Background(), h, datasets, columns)
	if err != nil {
		t.Fatalf("Profile() error = %v", err)
	}
	if len(profiles) != 3 {
		t.Fatalf("profiled %d columns, want 3 (PII column skipped, time column profiled as top values)", len(profiles))
	}
	for _, p := range profiles {
		if strings.Contains(strings.ToLower(p.Column), "email") {
			t.Errorf("PII column was profiled: %+v", p)
		}
	}
}

func TestParseModelResponseAcceptsValidQuestions(t *testing.T) {
	text := `["What is the total total amount?", "What are the most common customer id values?"]`
	got, err := ParseModelResponse(text, []string{"total_amount", "customer_id"}, []string{"revenue"})
	if err != nil {
		t.Fatalf("ParseModelResponse() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d suggestions, want 2", len(got))
	}
}

func TestParseModelResponseRejectsInventedEntities(t *testing.T) {
	// "salary" is not a known safe column or metric — the model invented it, so the question must
	// be dropped; the one valid question survives.
	text := `["What is the average salary?", "What is the total total amount?"]`
	got, err := ParseModelResponse(text, []string{"total_amount", "customer_id"}, []string{"revenue"})
	if err != nil {
		t.Fatalf("ParseModelResponse() error = %v", err)
	}
	if len(got) != 1 || got[0] != "What is the total total amount?" {
		t.Errorf("got %v, want only the entity-grounded question", got)
	}
}

func TestParseModelResponseRejectsGarbage(t *testing.T) {
	if _, err := ParseModelResponse("not json", []string{"amount"}, nil); err == nil {
		t.Error("ParseModelResponse() accepted non-JSON output")
	}
	// All-invented output is also an error (nothing usable).
	if _, err := ParseModelResponse(`["What is the salary?"]`, []string{"amount"}, nil); err == nil {
		t.Error("ParseModelResponse() accepted output with no grounded question")
	}
}

func TestParseModelResponseToleratesMarkdownFences(t *testing.T) {
	text := "```json\n[\"What is the total total amount?\", \"What are the most common customer id values?\"]\n```"
	got, err := ParseModelResponse(text, []string{"total_amount", "customer_id"}, nil)
	if err != nil {
		t.Fatalf("ParseModelResponse() error = %v on a fenced response", err)
	}
	if len(got) != 2 {
		t.Errorf("got %v, want both fenced questions", got)
	}
}

func TestProfileSurfacesQueryError(t *testing.T) {
	h := &fakeProfiler{err: errors.New("engine query failed")}
	_, err := Profile(context.Background(), h,
		[]domain.Dataset{{ID: "ds-1", OriginalFilename: "x.csv", IngestStatus: domain.IngestComplete}},
		[]domain.Column{{ID: "c-1", DatasetID: "ds-1", Name: "amount", InferredType: domain.TypeNumber}},
	)
	if err == nil {
		t.Error("Profile() returned nil error on a query failure")
	}
}

// nullNumericProfiler returns one row of all-NULL aggregates, mirroring DuckDB's MIN/MAX/AVG over an
// empty table or a fully-NULL column (standard SQL: aggregates without GROUP BY return one row).
type nullNumericProfiler struct{}

func (f *nullNumericProfiler) Query(_ context.Context, _ string, _ ...any) (engine.Rows, error) {
	return &fakeRows{data: [][]any{{nil, nil, nil}}}, nil
}

func TestProfileToleratesAllNullNumericColumn(t *testing.T) {
	// An all-NULL numeric column must NOT fail the whole generation with a scan error — it profiles
	// as name-and-type only (no observed range), and the other columns still profile normally.
	h := &nullNumericProfiler{}
	datasets := []domain.Dataset{{ID: "ds-1", OriginalFilename: "x.csv", IngestStatus: domain.IngestComplete}}
	columns := []domain.Column{
		{ID: "c-1", DatasetID: "ds-1", Name: "empty_amount", InferredType: domain.TypeNumber},
	}

	profiles, err := Profile(context.Background(), h, datasets, columns)
	if err != nil {
		t.Fatalf("Profile() error = %v, want nil for an all-NULL numeric column", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiled %d columns, want 1", len(profiles))
	}
	if profiles[0].Numeric != nil {
		t.Errorf("Numeric = %+v, want nil (no observed range for an all-NULL column)", profiles[0].Numeric)
	}
}
