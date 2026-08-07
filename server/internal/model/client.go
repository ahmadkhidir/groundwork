// This file is the outbound transport half of package model: the only code path that turns a
// sealed Payload (payload.go) into a network call to the Anthropic API. payload.go's package doc
// explains why Payload's unexported fields make this the only shape that can reach the wire;
// this file explains what happens at the wire itself.
package model

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultMaxTokens bounds each model call's response generation. A plan/generate_sql payload is
// small and structured; 4096 tokens is far beyond what either step needs and well under the
// per-model ceiling, so it serves as a single safety valve rather than a per-step knob. If it ever
// needs tuning per step or per environment, move it to config (behavior rule 12) — it is exported
// now so tests can see the value being sent.
const DefaultMaxTokens int64 = 4096

// AuditWriter is the egress-log seam Client depends on, satisfied by internal/audit.Writer.
//
// Narrower than audit.Writer itself on purpose: Client is allowed exactly one audit responsibility
// — recording the payloads it sends — and an interface with one method makes that impossible to
// widen accidentally.
type AuditWriter interface {
	WriteModelCall(ctx context.Context, workspaceID, userID, payload string) error
}

// UsageRecorder is the metering seam Client calls after each successful model call, satisfied by
// metering.UsageWriter (Record with engineSeconds 0). Deliberately narrow: the model package must
// only ever report the tokens it consumed, never read usage.
type UsageRecorder interface {
	Record(ctx context.Context, workspaceID string, engineSeconds float64, modelTokens int64) error
}

// Client is the single outbound path to the Anthropic API.
//
// Client.Complete is the seam payload.go's package doc names as the type-level enforcement of
// R-016: it accepts a Payload (never []byte or a string), so no caller can hand-assemble a body
// that bypassed BuildPayload.
type Client struct {
	apiKey    string
	model     anthropic.Model
	sdk       anthropic.Client
	audit     AuditWriter
	logger    *slog.Logger
	maxTokens int64
	// usage is nil unless WithUsageRecorder was supplied; Complete skips metering in that case.
	usage UsageRecorder
}

// NewClient constructs a Client for the given API key, writing egress entries through audit and
// logging through logger.
//
// opts are passed straight to anthropic.NewClient; tests use option.WithBaseURL to point the SDK at
// an httptest server. An empty apiKey is accepted (see config.Config.AnthropicAPIKey's doc) —
// Complete fails with a clear error rather than panicking.
func NewClient(apiKey string, audit AuditWriter, logger *slog.Logger, opts ...option.RequestOption) *Client {
	requestOpts := append([]option.RequestOption{option.WithHeader("x-api-key", apiKey)}, opts...)
	return &Client{
		apiKey:    apiKey,
		model:     anthropic.ModelClaudeHaiku4_5,
		sdk:       anthropic.NewClient(requestOpts...),
		audit:     audit,
		logger:    logger,
		maxTokens: DefaultMaxTokens,
	}
}

// WithUsageRecorder wires the metering seam into c and returns c for chaining. Optional: a Client
// without a recorder completes model calls as before, unmetered.
//
// A chainable setter rather than a constructor parameter because NewClient's variadic is the SDK's
// own option.RequestOption — adding a second variadic would be ambiguous. The setter keeps every
// existing call site and test compiling unchanged.
func (c *Client) WithUsageRecorder(u UsageRecorder) *Client {
	c.usage = u
	return c
}

// Complete sends payload to the model under the framing instructions system and returns the model's
// raw text response.
//
// Two load-bearing behaviors live here:
//
//  1. Egress logging happens in the SAME code path as the outbound call (behavior rule 7, FEAT-042):
//     the exact bytes about to be sent are written to the audit/egress log BEFORE the request is
//     issued, from Payload.JSON() — never from a summary or a redacted approximation. If the log
//     write fails, the call FAILS CLOSED: sending a payload with no egress record is precisely the
//     state FEAT-042's trust model exists to prevent, so a missing record is treated as a broken
//     call, not a best-effort aside.
//  2. The response is returned as raw text; each step parses it per StepName at its boundary
//     (domain/run.go's convention). Complete deliberately does not know what shape a step expects.
func (c *Client) Complete(ctx context.Context, workspaceID, userID, system string, payload Payload) (string, error) {
	if c.apiKey == "" {
		return "", errors.New("model: ANTHROPIC_API_KEY is not configured")
	}

	body, err := payload.JSON()
	if err != nil {
		return "", err
	}

	if err := c.audit.WriteModelCall(ctx, workspaceID, userID, body); err != nil {
		return "", fmt.Errorf("model: writing egress audit entry before model call: %w", err)
	}

	msg, err := c.sdk.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     c.model,
		MaxTokens: c.maxTokens,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(body))},
	})
	if err != nil {
		return "", fmt.Errorf("model: completing: %w", err)
	}

	// Metering event source: model tokens (input + output) consumed by this call. Best-effort by
	// design — a metering failure must NEVER fail a model call (metering is accounting, not the
	// fail-closed egress evidence above; a model call that succeeded must succeed regardless). A
	// background context with timeout (not the request ctx) maximizes the chance the write lands
	// after the client has disconnected, mirroring EngineStatusHook's best-effort side-effect pattern.
	if c.usage != nil {
		tokens := msg.Usage.InputTokens + msg.Usage.OutputTokens
		mctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.usage.Record(mctx, workspaceID, 0, tokens); err != nil {
			c.logger.Error("model: recording token usage",
				slog.String("workspace_id", workspaceID),
				slog.Int64("tokens", tokens),
				slog.String("error", err.Error()))
		}
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if block.Type == "text" && block.Text != "" {
			text.WriteString(block.Text)
		}
	}
	if text.Len() == 0 {
		return "", errors.New("model: response contained no text")
	}
	return text.String(), nil
}
