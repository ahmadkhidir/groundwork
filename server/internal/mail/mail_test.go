package mail

import (
	"strings"
	"testing"
)

func TestBuildMessageCarriesHeadersAndBody(t *testing.T) {
	msg := string(buildMessage("no-reply@example.com", "user@example.com", "Reset your Groundwork password", "body line"))

	for _, header := range []string{
		"From: no-reply@example.com",
		"To: user@example.com",
		"Subject: Reset your Groundwork password",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
	} {
		if !strings.Contains(msg, header) {
			t.Errorf("message lacks header %q:\n%s", header, msg)
		}
	}
	// Headers and body separated by a blank line (RFC 5322).
	if !strings.Contains(msg, "\r\n\r\nbody line") {
		t.Errorf("message lacks the header/body separator:\n%s", msg)
	}
	// A Date header must be present — some SMTP servers reject mail without one.
	if !strings.Contains(msg, "Date: ") {
		t.Errorf("message lacks a Date header:\n%s", msg)
	}
}

func TestResetLinkPointsAtResetPasswordWithTokenQuery(t *testing.T) {
	// screens/19: the reset token is read from the `token` query parameter of the emailed link.
	link := resetLink("https://app.example.com", "raw-token-abc")
	if link != "https://app.example.com/reset-password?token=raw-token-abc" {
		t.Errorf("resetLink() = %q", link)
	}
	// A trailing slash on the origin must not produce a double slash.
	if link := resetLink("https://app.example.com/", "t"); strings.Contains(link, "//reset-password") {
		t.Errorf("resetLink() produced a double slash: %q", link)
	}
}

func TestConfigEnabledRequiresHostAndFrom(t *testing.T) {
	if (Config{SMTPHost: "smtp.example.com"}).Enabled() {
		t.Error("Enabled() = true with no From address")
	}
	if (Config{From: "no-reply@example.com"}).Enabled() {
		t.Error("Enabled() = true with no SMTP host")
	}
	if !(Config{SMTPHost: "smtp.example.com", From: "no-reply@example.com"}).Enabled() {
		t.Error("Enabled() = false with both host and From set")
	}
}

func TestDeliverPasswordResetRejectsUnconfiguredClient(t *testing.T) {
	client := NewClient(Config{})
	if err := client.DeliverPasswordReset(t.Context(), "user@example.com", "token"); err == nil {
		t.Error("DeliverPasswordReset() on an unconfigured client returned nil, want an error")
	}
}
