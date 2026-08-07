package postgres

import (
	"context"
	"fmt"
)

// BillingEventRepository persists Stripe webhook events (billing milestone, pass 2).
//
// The billing_events table is BOTH the webhook idempotency guard and the durable billing-event
// record in one row: event_id (Stripe's own id) is the primary key, so a duplicate delivery of the
// same event can never insert twice, and each row records what the webhook applied for traceability
// (the audit_log_entries action_type enum has no billing type — see migrations/00010_stripe_billing.sql
// and the pass-2 report; this table is the documented-in-code alternative, not an enum extension).
type BillingEventRepository struct {
	pool *Pool
}

// NewBillingEventRepository constructs a BillingEventRepository backed by pool.
func NewBillingEventRepository(pool *Pool) *BillingEventRepository {
	return &BillingEventRepository{pool: pool}
}

// RecordIfNew inserts the event if its id has never been processed, reporting whether the insert
// happened. A false return means the event was already handled — Stripe delivers at-least-once, so
// the webhook handler treats "already recorded" as an acknowledged duplicate.
//
// The handler writes this row ONLY after the event's effects have been applied (all the tier /
// subscription writes are idempotent), so the row is a success marker rather than a claim: a crash
// between applying and recording leaves nothing recorded, Stripe retries, and the re-application is
// a no-op-safe replay. data is the JSON trace payload of what was applied.
func (r *BillingEventRepository) RecordIfNew(ctx context.Context, eventID, eventType, userID string, data []byte) (bool, error) {
	tag, err := r.pool.Exec(ctx,
		`INSERT INTO billing_events (event_id, event_type, user_id, data)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (event_id) DO NOTHING`,
		eventID, eventType, userID, data,
	)
	if err != nil {
		return false, fmt.Errorf("recording billing event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
