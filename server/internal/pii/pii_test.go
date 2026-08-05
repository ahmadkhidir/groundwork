package pii

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

func TestDetectFromNameFlagsKnownPatterns(t *testing.T) {
	cases := []struct {
		name        string
		wantFlagged bool
		wantPattern string
	}{
		{"email", true, PatternEmail},
		{"customer_email_address", true, PatternEmail},
		{"ssn", true, PatternNationalID},
		{"national_id_number", true, PatternNationalID},
		{"credit_card_number", true, PatternCardNumber},
		{"first_name", true, PatternName},
		{"customer_id", false, ""}, // "id" alone must never trip a name pattern — too generic
		{"username", false, ""},    // must not match on "name" alone
		{"order_total", false, ""},
	}
	for _, c := range cases {
		flagged, pattern := detectFromName(c.name)
		if flagged != c.wantFlagged || pattern != c.wantPattern {
			t.Errorf("detectFromName(%q) = (%v, %q), want (%v, %q)", c.name, flagged, pattern, c.wantFlagged, c.wantPattern)
		}
	}
}

func TestDetectFromSamplesFlagsConsistentEmailShape(t *testing.T) {
	samples := []string{"ada@example.com", "grace@example.com", "alan@example.com", ""}
	flagged, pattern := detectFromSamples(samples)
	if !flagged || pattern != PatternEmail {
		t.Errorf("got (%v, %q), want (true, %q) for a consistently email-shaped sample", flagged, pattern, PatternEmail)
	}
}

func TestDetectFromSamplesFlagsConsistentCardShape(t *testing.T) {
	samples := []string{"4111111111111111", "5500 0000 0000 0004", "340000000000009"}
	flagged, pattern := detectFromSamples(samples)
	if !flagged || pattern != PatternCardNumber {
		t.Errorf("got (%v, %q), want (true, %q) for card-shaped values", flagged, pattern, PatternCardNumber)
	}
}

func TestDetectFromSamplesFlagsConsistentSSNShape(t *testing.T) {
	samples := []string{"123-45-6789", "987-65-4321"}
	flagged, pattern := detectFromSamples(samples)
	if !flagged || pattern != PatternNationalID {
		t.Errorf("got (%v, %q), want (true, %q) for SSN-shaped values", flagged, pattern, PatternNationalID)
	}
}

func TestDetectFromSamplesIgnoresOneOffCoincidentalMatch(t *testing.T) {
	// Only one of five plain free-text values happens to contain an '@' + dot shape — below
	// matchThreshold, so the column as a whole must not be flagged.
	samples := []string{
		"delivered on time", "customer was happy", "contact ada@x.co about this",
		"no issues reported", "shipped early",
	}
	flagged, _ := detectFromSamples(samples)
	if flagged {
		t.Error("a single coincidental match must not flag the whole column")
	}
}

func TestDetectFromSamplesReturnsUnflaggedForEmptySample(t *testing.T) {
	flagged, pattern := detectFromSamples(nil)
	if flagged || pattern != "" {
		t.Errorf("got (%v, %q), want (false, \"\") for an empty sample", flagged, pattern)
	}
}

// acquireTestHandle leases a real engine handle — DetectAll's value sampling is meaningful only
// against an actual DuckDB instance, mirroring internal/profiling and internal/relationships' tests.
func acquireTestHandle(t *testing.T) engine.Handle {
	t.Helper()
	provider := engine.NewLocalProvider(time.Minute, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	handle, err := provider.Acquire(context.Background(), "ws-pii")
	if err != nil {
		t.Fatalf("acquiring engine: %v", err)
	}
	t.Cleanup(func() { _ = provider.Release(handle) })
	return handle
}

func writeParquetFixture(t *testing.T, h engine.Handle, path, selectSQL string) {
	t.Helper()
	stmt := "COPY (" + selectSQL + ") TO '" + path + "' (FORMAT parquet)"
	if _, err := h.Exec(context.Background(), stmt); err != nil {
		t.Fatalf("writing parquet fixture: %v", err)
	}
}

func TestDetectAllCombinesNameAndValueSignals(t *testing.T) {
	handle := acquireTestHandle(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "customers.parquet")

	writeParquetFixture(t, handle, path, `
		SELECT * FROM (VALUES
			('ada@example.com', 'Ada Lovelace', 42, 'blue')
		) AS t(contact_email, full_name, age, favorite_color)
	`)

	cols := []ColumnInput{
		{Name: "contact_email", InferredType: domain.TypeString},
		{Name: "full_name", InferredType: domain.TypeString},
		{Name: "age", InferredType: domain.TypeInteger},
		{Name: "favorite_color", InferredType: domain.TypeString},
	}

	results, err := DetectAll(context.Background(), handle, path, cols)
	if err != nil {
		t.Fatalf("DetectAll() error = %v", err)
	}

	if !results["contact_email"].Flagged || results["contact_email"].Pattern != PatternEmail {
		t.Errorf("contact_email = %+v, want flagged as email (from its name)", results["contact_email"])
	}
	if !results["full_name"].Flagged || results["full_name"].Pattern != PatternName {
		t.Errorf("full_name = %+v, want flagged as name (from its name)", results["full_name"])
	}
	if results["age"].Flagged {
		t.Errorf("age = %+v, want unflagged", results["age"])
	}
	if results["favorite_color"].Flagged {
		t.Errorf("favorite_color = %+v, want unflagged", results["favorite_color"])
	}
}
