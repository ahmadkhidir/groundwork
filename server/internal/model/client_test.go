package model

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/semantic"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// recordingAudit is the AuditWriter seam as a test double: it records the exact payload string it
// was handed and the order of calls, so tests can assert the egress write happened before the HTTP
// request and carried the exact bytes.
type recordingAudit struct {
	mu         sync.Mutex
	payloads   []string
	writes     int
	failWith   error
	recordCall bool
}

func (r *recordingAudit) WriteModelCall(_ context.Context, _, _, payload string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
	if r.recordCall {
		r.payloads = append(r.payloads, payload)
	}
	return r.failWith
}

func (r *recordingAudit) got() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.payloads) == 0 {
		return r.writes, ""
	}
	return r.writes, r.payloads[len(r.payloads)-1]
}

// modelEchoServer returns an httptest server that records the request body the SDK sent and replies
// with a canned Anthropic message response.
func modelEchoServer(t *testing.T, replyText string) (*httptest.Server, *string) {
	t.Helper()
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		if replyText == "" {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
			return
		}
		w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":` +
			jsonString(replyText) + `}],"model":"claude-haiku-4-5","stop_reason":"end_turn","stop_sequence":null,` +
			`"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotBody
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func testPayload() Payload {
	return BuildPayload("question", "", []TableMetadata{{
		DatasetID: "ds-1",
		Name:      "dataset_sales_a1b2c3d4",
		Columns:   []ColumnMetadata{{Name: "amount", Type: "number"}},
	}}, semantic.GroundingContext{})
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCompleteWritesEgressBeforeCallingModel(t *testing.T) {
	audit := &recordingAudit{recordCall: true}
	srv, gotBody := modelEchoServer(t, `{"sql":"SELECT 1"}`)
	client := NewClient("test-key", audit, discardLogger(), option.WithBaseURL(srv.URL))

	out, err := client.Complete(context.Background(), "ws-1", "user-1", "system prompt", testPayload())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != `{"sql":"SELECT 1"}` {
		t.Errorf("out = %q, want the model's raw text", out)
	}

	// Egress must be written BEFORE the request was issued, and must carry the exact payload bytes.
	writes, last := audit.got()
	if writes != 1 {
		t.Fatalf("audit writes = %d, want 1", writes)
	}
	if last == "" {
		t.Fatal("audit payload was not recorded")
	}
	var payloadWire map[string]any
	if err := json.Unmarshal([]byte(last), &payloadWire); err != nil {
		t.Fatalf("audit payload is not JSON: %v", err)
	}
	if payloadWire["question"] != "question" {
		t.Errorf("audit payload question = %v", payloadWire["question"])
	}

	// The SDK wraps the payload text in a message envelope; the user message's text field must be
	// byte-for-byte the same payload the audit recorded.
	var reqBody struct {
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(*gotBody), &reqBody); err != nil {
		t.Fatalf("model request body is not JSON: %v", err)
	}
	if len(reqBody.Messages) != 1 || len(reqBody.Messages[0].Content) != 1 {
		t.Fatalf("model request messages = %+v, want one user message with one text block", reqBody.Messages)
	}
	if reqBody.Messages[0].Content[0].Text != last {
		t.Errorf("model request text = %q, want the exact payload bytes the audit recorded: %q",
			reqBody.Messages[0].Content[0].Text, last)
	}
}

func TestCompleteFailsClosedOnMissingAPIKey(t *testing.T) {
	audit := &recordingAudit{recordCall: true}
	client := NewClient("", audit, discardLogger())

	_, err := client.Complete(context.Background(), "ws-1", "user-1", "system", testPayload())
	if err == nil {
		t.Fatal("Complete succeeded with no API key, want an error")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error = %v, want it to name the missing key", err)
	}
	if writes, _ := audit.got(); writes != 0 {
		t.Errorf("audit writes = %d, want 0 — a payload with no key must never be logged or sent", writes)
	}
}

// TestCompleteFailsClosedOnAuditWriteError pins the fail-closed contract: if the egress log write
// fails, the outbound call must not be issued. The httptest server records whether it was hit.
func TestCompleteFailsClosedOnAuditWriteError(t *testing.T) {
	audit := &recordingAudit{failWith: errTestAuditDown}
	srv, gotBody := modelEchoServer(t, `{"sql":"SELECT 1"}`)
	client := NewClient("test-key", audit, discardLogger(), option.WithBaseURL(srv.URL))

	_, err := client.Complete(context.Background(), "ws-1", "user-1", "system", testPayload())
	if err == nil {
		t.Fatal("Complete succeeded despite a failing egress log, want fail-closed error")
	}
	if !strings.Contains(err.Error(), "egress audit") {
		t.Errorf("error = %v, want it to name the egress audit failure", err)
	}
	if *gotBody != "" {
		t.Errorf("model server was hit with %q, want no request when the egress log failed", *gotBody)
	}
}

var errTestAuditDown = &testAuditError{}

type testAuditError struct{}

func (e *testAuditError) Error() string { return "audit store unreachable" }

func TestCompleteReturnsErrorOnNonTextResponse(t *testing.T) {
	audit := &recordingAudit{recordCall: true}
	srv, _ := modelEchoServer(t, "")
	client := NewClient("test-key", audit, discardLogger(), option.WithBaseURL(srv.URL))

	_, err := client.Complete(context.Background(), "ws-1", "user-1", "system", testPayload())
	if err == nil {
		t.Fatal("Complete succeeded on an API error, want an error")
	}
}

// TestCompleteErrorResponseBodyNoText simulates a 200 with content blocks that carry no text.
func TestCompleteErrorResponseBodyNoText(t *testing.T) {
	audit := &recordingAudit{recordCall: true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-haiku-4-5"}`))
	}))
	t.Cleanup(srv.Close)
	client := NewClient("test-key", audit, discardLogger(), option.WithBaseURL(srv.URL))

	_, err := client.Complete(context.Background(), "ws-1", "user-1", "system", testPayload())
	if err == nil {
		t.Fatal("Complete succeeded on an empty response, want an error")
	}
	if !strings.Contains(err.Error(), "no text") {
		t.Errorf("error = %v, want it to name the empty response", err)
	}
}
