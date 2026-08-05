// Package auth implements FEAT-001 account creation, credential verification, and password reset.
//
// This package owns every credential-handling concern for the service: hashing, verification,
// signup/login orchestration, and reset-token issuance. Nothing outside this package touches a raw
// or hashed password.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters, per OWASP's Password Storage Cheat Sheet (the "m=64MiB, t=3, p=4" profile).
// Argon2id — not bcrypt, and never a SHA-family digest — is mandatory for credential storage: it is
// memory-hard, which is the property that makes GPU/ASIC cracking expensive.
const (
	argonMemoryKiB   uint32 = 64 * 1024
	argonIterations  uint32 = 3
	argonParallelism uint8  = 4
	argonSaltLen            = 16
	argonKeyLen      uint32 = 32
)

// errMalformedHash indicates a stored hash does not match the expected encoding. This should never
// happen against data written by HashPassword; seeing it means the users table was written by
// something else, or corrupted.
var errMalformedHash = errors.New("malformed password hash")

// HashPassword derives an Argon2id hash for password, encoded in a self-describing string:
//
//	$argon2id$v=19$m=65536,t=3,p=4$<base64 salt>$<base64 hash>
//
// Encoding the parameters alongside the hash lets them be tuned later (e.g. as hardware gets
// faster) without invalidating hashes written under the old parameters — VerifyPassword reads them
// back out of the string rather than assuming the current constants.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonParallelism, argonKeyLen)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)
	return encoded, nil
}

// VerifyPassword reports whether password matches encodedHash.
//
// The comparison is constant-time: a timing difference between "close" and "far" hash mismatches
// is itself an oracle an attacker can use to narrow down a password character by character.
func VerifyPassword(encodedHash, password string) (bool, error) {
	version, memory, iterations, parallelism, salt, hash, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}
	if version != argon2.Version {
		return false, errMalformedHash
	}

	candidate := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(hash)))
	return subtle.ConstantTimeCompare(candidate, hash) == 1, nil
}

// decodeHash parses the string format written by HashPassword.
func decodeHash(encoded string) (version int, memory, iterations uint32, parallelism uint8, salt, hash []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return 0, 0, 0, 0, nil, nil, errMalformedHash
	}
	if _, err = fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return 0, 0, 0, 0, nil, nil, errMalformedHash
	}
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return 0, 0, 0, 0, nil, nil, errMalformedHash
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return 0, 0, 0, 0, nil, nil, errMalformedHash
	}
	if hash, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return 0, 0, 0, 0, nil, nil, errMalformedHash
	}
	return version, memory, iterations, parallelism, salt, hash, nil
}
