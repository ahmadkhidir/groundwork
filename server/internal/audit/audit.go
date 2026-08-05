// Package audit implements the combined audit and model-egress log (FEAT-042).
//
// One writer, used by every action's own code path — never a background reconciler. behavior rule 7
// (IMPLEMENTATION.md §5): "every payload sent to the model is written to the audit/egress log in the
// same code path as the outbound call... never reconciled after the fact." The same discipline
// applies to every other action type this package writes: a query, an export, a deletion, a login
// are logged at the moment they happen, by the code that performs them, not by an observer that
// could drift from what actually occurred.
package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// Store is the persistence seam Writer depends on, satisfied by internal/store/postgres.AuditRepository.
// Defined consumer-side so this package is unit-testable without importing the Postgres driver.
type Store interface {
	Create(ctx context.Context, entry domain.AuditLogEntry) error
}

// Writer appends entries to the combined audit/egress log.
type Writer struct {
	store Store
	clock func() time.Time
}

// NewWriter constructs a Writer backed by store.
func NewWriter(store Store) *Writer {
	return &Writer{store: store, clock: time.Now}
}

// WriteLogin records an account-scoped login entry.
//
// workspace_id is deliberately empty: authentication happens before any workspace is selected, and
// schema.md's Relationships note is explicit that login is never assigned a placeholder workspace to
// satisfy a required field. See migrations/00002_audit_log_account_scope.sql, which relaxed the
// column specifically for this write.
func (w *Writer) WriteLogin(ctx context.Context, userID string) error {
	return w.write(ctx, domain.AuditLogEntry{
		WorkspaceID: "",
		UserID:      userID,
		ActionType:  domain.ActionLogin,
	})
}

// WriteQueryExecuted records a workspace-scoped query execution. Reserved for FEAT-013 (Phase 2);
// exposed now so the run-execution code path can call straight into this Writer without a second
// audit entry point being invented later.
func (w *Writer) WriteQueryExecuted(ctx context.Context, workspaceID, userID string) error {
	return w.write(ctx, domain.AuditLogEntry{
		WorkspaceID: workspaceID,
		UserID:      userID,
		ActionType:  domain.ActionQueryExecuted,
	})
}

// WriteModelCall records the exact payload sent to the model provider. Reserved for FEAT-013
// (Phase 2). payload must be the literal outbound request body — this is the log FEAT-042's user
// story exists to make trustworthy, so it is never a summary or a redacted approximation.
func (w *Writer) WriteModelCall(ctx context.Context, workspaceID, userID, payload string) error {
	return w.write(ctx, domain.AuditLogEntry{
		WorkspaceID:        workspaceID,
		UserID:             userID,
		ActionType:         domain.ActionModelCall,
		PayloadSentToModel: payload,
	})
}

// WriteExport records a report/data export. Reserved for FEAT-035 (Phase 4).
func (w *Writer) WriteExport(ctx context.Context, workspaceID, userID string) error {
	return w.write(ctx, domain.AuditLogEntry{
		WorkspaceID: workspaceID,
		UserID:      userID,
		ActionType:  domain.ActionExport,
	})
}

// WriteDeletion records a workspace deletion. Reserved for FEAT-043 (Phase 1, not this wave).
func (w *Writer) WriteDeletion(ctx context.Context, workspaceID, userID string) error {
	return w.write(ctx, domain.AuditLogEntry{
		WorkspaceID: workspaceID,
		UserID:      userID,
		ActionType:  domain.ActionDeletion,
	})
}

func (w *Writer) write(ctx context.Context, entry domain.AuditLogEntry) error {
	entry.ID = uuid.NewString()
	entry.Timestamp = w.clock()
	if err := w.store.Create(ctx, entry); err != nil {
		return fmt.Errorf("audit: writing entry: %w", err)
	}
	return nil
}
