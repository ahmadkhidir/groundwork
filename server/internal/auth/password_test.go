package auth

import "testing"

func TestHashPasswordAndVerifyPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	// Never SHA-family, never plaintext: the stored form must be the self-describing Argon2id
	// encoding, not the password itself.
	if hash == "correct horse battery staple" {
		t.Fatal("HashPassword() returned the plaintext password unchanged")
	}

	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil {
		t.Fatalf("VerifyPassword() error = %v", err)
	}
	if !ok {
		t.Error("VerifyPassword() = false, want true for the matching password")
	}
}

func TestVerifyPasswordRejectsWrongPassword(t *testing.T) {
	hash, err := HashPassword("the-real-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	ok, err := VerifyPassword(hash, "a-different-password")
	if err != nil {
		t.Fatalf("VerifyPassword() error = %v", err)
	}
	if ok {
		t.Error("VerifyPassword() = true, want false for a non-matching password")
	}
}

func TestHashPasswordProducesUniqueSaltsForIdenticalPasswords(t *testing.T) {
	first, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	second, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	// Two hashes of the same password must differ (random salt), or an attacker with the users
	// table could spot repeated passwords across accounts.
	if first == second {
		t.Error("HashPassword() produced identical output for two calls with the same password")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	tests := []string{
		"",
		"not-a-hash-at-all",
		"$argon2id$v=19$m=65536,t=3,p=4$onlysalt",
		"$bcrypt$2a$10$notargon2$hash",
	}

	for _, hash := range tests {
		t.Run(hash, func(t *testing.T) {
			if _, err := VerifyPassword(hash, "anything"); err == nil {
				t.Error("VerifyPassword() error = nil, want an error for a malformed hash")
			}
		})
	}
}
