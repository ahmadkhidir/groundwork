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
