// Package suggestions implements FEAT-050 (Question Suggestions): deterministic, metadata-only
// generation of 5-10 suggested questions from a workspace's persisted datasets, columns,
// relationships, and metric definitions.
//
// By design there is NO engine query and NO model call (FEAT-050's core acceptance criteria):
// everything is a Postgres read plus template expansion. That makes the feature instant on a
// sleeping engine, free on every tier, egress-free (no new model_call audit entry), and structurally
// honest — a suggestion is composed from real, confirmed, safe persisted rows, so it can never
// reference a column that does not exist, an unconfirmed relationship, or an undefined metric, and
// it is always a question, never a claim.
package suggestions

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// Source is the persistence seam Generate reads. Every method is satisfied by the corresponding
// internal/store/postgres repository; defining the interface here (consumer-side) keeps this
// package unit-testable against a fake without importing the Postgres driver.
type Source interface {
	Datasets(ctx context.Context, workspaceID string) ([]domain.Dataset, error)
	Columns(ctx context.Context, workspaceID string) ([]domain.Column, error)
	Relationships(ctx context.Context, workspaceID string) ([]domain.Relationship, error)
	Metrics(ctx context.Context, workspaceID string) ([]domain.MetricDefinition, error)
}

// maxSuggestions caps the strip at FEAT-050's "5-10" band's upper bound. The lower bound is NOT
// enforced: a workspace with few safe columns yields fewer suggestions, honestly, never padded.
const maxSuggestions = 10

// safeColumn is a column that passed the sensitivity/ingest filters, paired with its dataset for
// same-dataset pairing in the type-aware templates.
type safeColumn struct {
	col     domain.Column
	dataset domain.Dataset
}

// Generate returns the deterministic suggested questions for workspaceID.
//
// Deterministic by construction: inputs are sorted before generation, templates expand in a fixed
// order, and the result is de-duplicated — regenerating on unchanged metadata returns the same set
// in the same order (FEAT-050 AC: "no cache required"). May return fewer than 5 (or zero) for a
// workspace whose safe columns are few — honesty over padding.
func Generate(ctx context.Context, workspaceID string, src Source) ([]string, error) {
	datasets, err := src.Datasets(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing datasets for suggestions: %w", err)
	}
	columns, err := src.Columns(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing columns for suggestions: %w", err)
	}
	rels, err := src.Relationships(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing relationships for suggestions: %w", err)
	}
	metrics, err := src.Metrics(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing metric definitions for suggestions: %w", err)
	}

	// Deterministic input order regardless of store ordering.
	sort.Slice(datasets, func(i, j int) bool { return datasets[i].ID < datasets[j].ID })
	sort.Slice(columns, func(i, j int) bool { return columns[i].ID < columns[j].ID })
	sort.Slice(rels, func(i, j int) bool { return rels[i].ID < rels[j].ID })
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].ID < metrics[j].ID })

	// Only fully ingested datasets contribute; their metadata is the pool everything is drawn from.
	complete := make(map[string]domain.Dataset, len(datasets))
	for _, d := range datasets {
		if d.IngestStatus == domain.IngestComplete {
			complete[d.ID] = d
		}
	}

	var numeric, categorical, temporal, timeOfDay []safeColumn
	colByID := make(map[string]domain.Column, len(columns))
	for _, c := range columns {
		// FEAT-050: a PII-flagged or model-excluded column is never referenced, even by name.
		if c.PIIFlag || c.ExcludedFromModel {
			continue
		}
		ds, ok := complete[c.DatasetID]
		if !ok {
			continue
		}
		colByID[c.ID] = c
		sc := safeColumn{col: c, dataset: ds}
		switch c.InferredType {
		case domain.TypeInteger, domain.TypeNumber:
			numeric = append(numeric, sc)
		case domain.TypeDate, domain.TypeTimestamp:
			temporal = append(temporal, sc)
		case domain.TypeTime:
			// A bare time-of-day column (trading data's split Date + Time) generates time-of-day
			// questions rather than trend questions, per FEAT-050's type-aware template rule.
			timeOfDay = append(timeOfDay, sc)
		case domain.TypeString, domain.TypeBoolean:
			categorical = append(categorical, sc)
		}
	}

	var out []string
	seen := make(map[string]bool)
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	// Aggregate questions from numeric columns, paired with a same-dataset categorical or temporal
	// column where one exists (type-aware per FEAT-050).
	for _, nc := range numeric {
		name := displayName(nc.col.Name)
		add("What is the total " + name + "?")
		add("What is the average " + name + "?")
		add("What is the highest " + name + "?")
		for _, cat := range sameDataset(categorical, nc.dataset.ID) {
			catName := displayName(cat.col.Name)
			add("What is the " + name + " for each " + catName + "?")
			add("What are the top " + catName + " by " + name + "?")
		}
		for range sameDataset(temporal, nc.dataset.ID) {
			add("How does " + name + " change over time?")
		}
		// Time-of-day pairing, FEAT-050: a numeric column with a same-dataset time column yields a
		// time-of-day question ("What is the volume by time of day?"), the trading-data case where
		// date and time are split into separate columns.
		if len(sameDataset(timeOfDay, nc.dataset.ID)) > 0 {
			add("What is the " + name + " by time of day?")
		}
	}

	// Time-of-day questions from time columns (e.g. trading session times), FEAT-050.
	for _, tc := range timeOfDay {
		name := displayName(tc.col.Name)
		add("What are the most common " + name + " values?")
	}

	// Distribution / top-value questions from categorical columns.
	for _, cat := range categorical {
		name := displayName(cat.col.Name)
		add("What are the most common " + name + " values?")
		add("How many distinct " + name + " values are there?")
	}

	// Metric questions by defined metric name.
	for _, m := range metrics {
		add("What is the " + m.Name + "?")
		if len(categorical) > 0 {
			add("What is the " + m.Name + " by " + displayName(categorical[0].col.Name) + "?")
		}
	}

	// Cross-dataset questions from confirmed relationships, using the resolved column names.
	for _, rel := range rels {
		source, okS := colByID[rel.SourceColumnID]
		target, okT := colByID[rel.TargetColumnID]
		if !okS || !okT {
			continue
		}
		add("How do " + displayName(source.Name) + " and " + displayName(target.Name) + " relate across the linked datasets?")
	}

	if len(out) > maxSuggestions {
		out = out[:maxSuggestions]
	}
	return out, nil
}

// sameDataset filters a column group down to the columns belonging to datasetID.
func sameDataset(group []safeColumn, datasetID string) []safeColumn {
	var out []safeColumn
	for _, sc := range group {
		if sc.dataset.ID == datasetID {
			out = append(out, sc)
		}
	}
	return out
}

// displayName renders a column/dataset identifier as plain business language for a suggestion:
// underscores and dashes become spaces. The name's case is preserved — an uppercase business label
// (CUSTOMER_NAME, TRXN_AMT) reads as the label the user already sees in their data.
func displayName(name string) string {
	return strings.NewReplacer("_", " ", "-", " ").Replace(name)
}
