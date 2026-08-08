// Package mail sends the product's transactional email — today, only password-reset link delivery
// (architecture.md's Transactional Email Provider, the "only outbound email the product sends").
//
// Provider-agnostic by design: SMTP is the interface every transactional email provider exposes, so
// the vendor choice is a configuration value (SMTP_HOST/SMTP_USER/...) rather than code. When no
// SMTP host is configured the client is disabled and the caller keeps its fallback (the structured
// log line an operator relays), matching architecture.md's Medium risk note: account recovery is
// blocked without a provider, but no other flow depends on it.
package mail

import (
	"context"
	"errors"
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

// Config is the SMTP endpoint reset mail is delivered through, resolved from the environment by
// internal/config (MailConfig). WebBaseURL is the origin reset links are built from.
type Config struct {
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	From         string
	WebBaseURL   string
}

// Enabled reports whether SMTP delivery is configured. A host AND a from address are required;
// either missing means "no provider wired up" and the caller keeps its log-line fallback.
func (c Config) Enabled() bool {
	return c.SMTPHost != "" && c.From != ""
}

// Client sends password-reset email over SMTP.
type Client struct {
	cfg Config
}

// NewClient constructs a Client for cfg.
func NewClient(cfg Config) *Client {
	return &Client{cfg: cfg}
}

// DeliverPasswordReset sends the reset link for token to to. The link points at the web tier's
// /reset-password route with the raw token in the `token` query parameter (screens/19); only the
// token's hash is ever persisted (schema.md PasswordResetToken.token_hash).
//
// Returns an error when SMTP is not configured or the send fails. Callers treat this as
// best-effort: delivery failure must never fail the reset-request flow (architecture.md's
// fire-and-forget contract), and the token stays in the structured log line as the relay fallback.
func (c *Client) DeliverPasswordReset(_ context.Context, to, token string) error {
	if !c.cfg.Enabled() {
		return errors.New("mail: SMTP is not configured")
	}

	link := resetLink(c.cfg.WebBaseURL, token)
	subject := "Reset your Groundwork password"
	body := fmt.Sprintf(
		"Hi,\n\nSomeone requested a password reset for your Groundwork account. If that was you, "+
			"open this link to choose a new password — it expires in 30 minutes:\n\n%s\n\n"+
			"If you didn't request this, you can safely ignore this email; the link cannot be used "+
			"without being opened from this email.\n\n— Groundwork",
		link,
	)

	addr := fmt.Sprintf("%s:%d", c.cfg.SMTPHost, c.cfg.SMTPPort)
	auth := smtp.PlainAuth("", c.cfg.SMTPUser, c.cfg.SMTPPassword, c.cfg.SMTPHost)
	return smtp.SendMail(addr, auth, c.cfg.From, []string{to}, buildMessage(c.cfg.From, to, subject, body))
}

// resetLink builds the full reset URL from the web origin and the raw token.
func resetLink(webBaseURL, token string) string {
	return fmt.Sprintf("%s/reset-password?token=%s", strings.TrimRight(webBaseURL, "/"), token)
}

// buildMessage renders a minimal RFC 5322 message: headers, a blank line, then the body. The Date
// header matters — some SMTP servers reject mail without one. Content-Type is text/plain with the
// utf-8 charset so the body's plain-ASCII copy stays unambiguous.
func buildMessage(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}
