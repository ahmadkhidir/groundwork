package auth

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// emailPattern is a deliberately permissive format check — RFC 5322 in full is not worth
// implementing here, and screens/02-sign-up-login.md's Validation Error state only names "invalid
// email format" as a condition, not a specific grammar.
var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// Validation errors matching exactly the two conditions screens/02-sign-up-login.md's Validation
// Error state names: invalid email format, and an empty required field. No password complexity
// policy is specified anywhere in the docs, so none is invented here.
var (
	ErrInvalidEmail     = errors.New("enter a valid email address")
	ErrPasswordRequired = errors.New("password is required")
)

// normalizeEmail lowercases and trims an email so "User@Example.com" and "user@example.com " are
// treated as the same account both at signup and at login.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func validateEmail(email string) error {
	if !emailPattern.MatchString(email) {
		return ErrInvalidEmail
	}
	return nil
}

func validatePassword(password string) error {
	if password == "" {
		return ErrPasswordRequired
	}
	return nil
}

// deriveDisplayName produces a default display name from an email address.
//
// GAP (reported upstream): schema.md's User entity requires display_name, but
// screens/02-sign-up-login.md's Sign Up form collects only email and password — no name field
// exists to populate it from. Deriving a readable default from the email's local part is the
// smallest defensible behavior until the docs specify a real name-collection step.
func deriveDisplayName(email string) string {
	local := email
	if idx := strings.IndexByte(email, '@'); idx > 0 {
		local = email[:idx]
	}
	local = strings.ReplaceAll(local, ".", " ")
	local = strings.ReplaceAll(local, "_", " ")

	words := strings.Fields(local)
	if len(words) == 0 {
		return "Groundwork User"
	}
	for i, word := range words {
		r := []rune(word)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}
