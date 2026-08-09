// Package authsession decodes the Auth.js (next-auth v5) session cookie so the Go API service can
// authenticate requests the browser sends directly to it.
//
// ARCHITECTURAL CONTEXT — read before touching this package.
//
// architecture.md's boundary rule gives the Next.js tier session MANAGEMENT (it owns sign-in, the
// cookie, and its lifecycle) while requiring the Go API service to independently re-verify identity
// on every request ("enforced on every request by the Go API Service"). web/src/proxy.ts states this
// explicitly: its own check is "optimistic only... not the authorization boundary."
//
// web/src/lib/api-client.ts calls the Go API directly from the browser. The credential that
// travels is the Auth.js session token, by one of two transports:
//
//   - Bearer header (production, Vercel web + Fly API): the two tiers have no shared parent domain
//     for a cross-origin cookie, so the web tier's BFF route (web/src/app/api/session-token) hands
//     the browser the raw Auth.js JWE and the browser sends it as `Authorization: Bearer`. The web
//     tier pins the session cookie to the plain name (web/src/lib/session-cookie.ts), so the JWE's
//     HKDF salt is always "authjs.session-token" regardless of transport.
//   - Cookie (local development): both tiers on `localhost` (different ports) share a host, so the
//     host-only Auth.js cookie is delivered to the API's origin unprompted.
//
// Given that, the only remaining piece is being able to READ the token. Auth.js v5 encrypts it by
// default as a JWE (RFC 7518 "A256CBC-HS512", "dir" key management) derived from AUTH_SECRET via
// HKDF-SHA256, exactly as implemented in web/node_modules/@auth/core/jwt.js's `encode`/`decode` and
// `getDerivedEncryptionKey`. This package is a byte-for-byte Go port of that decode path — verified
// against a real token produced by that exact library (see session_test.go).
//
// AUTH_SECRET itself is read directly from the environment here rather than through
// server/internal/config, because that package is read-only to this wave (see IMPLEMENTATION.md
// §5 rule 16). Ideally AuthSecret becomes a proper config.Config field — reported as a needed
// Foundation change in this wave's report.
package authsession

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
)

// CookieName is the non-secure (local development / plain HTTP) name Auth.js uses for its session
// cookie. SecureCookieName is used instead once the deployment serves HTTPS.
const (
	CookieName       = "authjs.session-token"
	SecureCookieName = "__Secure-authjs.session-token"
)

// ErrNoSession indicates no session cookie was present on the request.
var ErrNoSession = errors.New("authsession: no session cookie present")

// ErrInvalidSession indicates a session cookie was present but failed to decrypt or verify — an
// expired, tampered, or foreign token. Deliberately one error for every failure mode: like
// auth.Service.Authenticate's login handling, callers must not let decoding failures leak *why* a
// session was rejected.
var ErrInvalidSession = errors.New("authsession: invalid or expired session")

// Claims is the subset of the Auth.js JWT payload this service needs.
//
// tier rides on the token purely as a display convenience upstream; per web/src/auth.ts's own
// comment, it must never be trusted as the sole authority for a gated action. Every handler in this
// codebase that enforces a tier limit (e.g. workspace creation) re-reads the authoritative value
// from Postgres via the existing UserRepository rather than trusting Claims.Tier.
type Claims struct {
	UserID string `json:"id"`
	Tier   string `json:"tier"`
	Email  string `json:"email"`
}

// FromRequest extracts and decodes the Auth.js session token from r, verifying it against secret.
//
// The token is accepted from `Authorization: Bearer` (production — the web tier's BFF route hands
// the browser the raw JWE, since a cross-origin cookie cannot travel from Vercel to the API's
// domain) or from the session cookie (local development, where both tiers share `localhost`).
// The bearer token's salt is always the plain cookie name, which the web tier pins. Returns
// ErrNoSession if no token is present, ErrInvalidSession for any decode/verification failure.
func FromRequest(r *http.Request, secret string) (Claims, error) {
	if token := bearerToken(r); token != "" {
		return decodeAndVerify(token, secret, CookieName)
	}

	cookieName := CookieName
	c, err := r.Cookie(cookieName)
	if err != nil {
		cookieName = SecureCookieName
		c, err = r.Cookie(cookieName)
		if err != nil {
			return Claims{}, ErrNoSession
		}
	}
	return decodeAndVerify(c.Value, secret, cookieName)
}

// bearerToken returns the `Authorization: Bearer <token>` value, or "" when absent or not a Bearer
// header. The scheme comparison is case-insensitive per RFC 7235.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// decodeAndVerify decrypts an Auth.js JWE and validates its claims. The JWE decrypts with the
// shared AUTH_SECRET and the salt the token was encoded with (the cookie name); the payload must
// carry a non-empty user id and an unexpired `exp` claim. Expiry is load-bearing here: the token is
// browser-visible in the bearer transport, so an expired JWE — which still decrypts fine — must be
// rejected rather than trusted.
func decodeAndVerify(token, secret, salt string) (Claims, error) {
	payload, err := decode(token, secret, salt)
	if err != nil {
		return Claims{}, ErrInvalidSession
	}

	var p struct {
		UserID string `json:"id"`
		Tier   string `json:"tier"`
		Email  string `json:"email"`
		Exp    int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return Claims{}, ErrInvalidSession
	}
	if p.UserID == "" {
		return Claims{}, ErrInvalidSession
	}
	// Auth.js always sets exp (from maxAge); a token without one is not a token it issued.
	if p.Exp == 0 || p.Exp < time.Now().Unix() {
		return Claims{}, ErrInvalidSession
	}
	return Claims{UserID: p.UserID, Tier: p.Tier, Email: p.Email}, nil
}

// decode reverses @auth/core/jwt's `encode`: a compact JWE using "dir" key management and
// "A256CBC-HS512" content encryption, whose key is HKDF-SHA256(secret, salt, info) where salt is
// the cookie name and info is the literal string Auth.js derives it with.
func decode(token, secret, salt string) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 5 {
		return nil, fmt.Errorf("authsession: malformed JWE: expected 5 segments, got %d", len(parts))
	}
	headerB64, encryptedKeyB64, ivB64, ciphertextB64, tagB64 := parts[0], parts[1], parts[2], parts[3], parts[4]

	if encryptedKeyB64 != "" {
		// "dir" key management carries no encrypted key segment. A non-empty one here means this
		// token was not produced with alg=dir, which is the only algorithm Auth.js v5 issues.
		return nil, errors.New("authsession: unexpected encrypted key segment for \"dir\" algorithm")
	}

	headerJSON, err := base64URLDecode(headerB64)
	if err != nil {
		return nil, fmt.Errorf("authsession: decoding header: %w", err)
	}

	var header struct {
		Alg string `json:"alg"`
		Enc string `json:"enc"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("authsession: parsing header: %w", err)
	}
	if header.Alg != "dir" || header.Enc != "A256CBC-HS512" {
		return nil, fmt.Errorf("authsession: unsupported alg/enc %q/%q", header.Alg, header.Enc)
	}

	iv, err := base64URLDecode(ivB64)
	if err != nil {
		return nil, fmt.Errorf("authsession: decoding iv: %w", err)
	}
	ciphertext, err := base64URLDecode(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("authsession: decoding ciphertext: %w", err)
	}
	tag, err := base64URLDecode(tagB64)
	if err != nil {
		return nil, fmt.Errorf("authsession: decoding tag: %w", err)
	}

	cek, err := derivedEncryptionKey(secret, salt)
	if err != nil {
		return nil, err
	}

	// RFC 7518 §5.2.2.1: for A256CBC-HS512 the 64-byte CEK splits into a 32-byte MAC_KEY (the
	// initial octets) and a 32-byte ENC_KEY (the final octets).
	macKey, encKey := cek[:32], cek[32:]

	// AAD is the ASCII bytes of the base64url-encoded protected header — the header segment
	// verbatim as it appeared on the wire, not the decoded JSON.
	aad := []byte(headerB64)
	if err := verifyTag(macKey, aad, iv, ciphertext, tag); err != nil {
		return nil, err
	}

	plaintext, err := aesCBCDecrypt(encKey, iv, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("authsession: decrypting payload: %w", err)
	}
	return plaintext, nil
}

// derivedEncryptionKey reproduces getDerivedEncryptionKey in @auth/core/jwt.js: a 64-byte key via
// HKDF-SHA256(secret, salt, "Auth.js Generated Encryption Key (<salt>)").
func derivedEncryptionKey(secret, salt string) ([]byte, error) {
	info := fmt.Sprintf("Auth.js Generated Encryption Key (%s)", salt)

	kdf := hkdf.New(sha256.New, []byte(secret), []byte(salt), []byte(info))
	key := make([]byte, 64)
	if _, err := io.ReadFull(kdf, key); err != nil {
		return nil, fmt.Errorf("authsession: deriving key: %w", err)
	}
	return key, nil
}

// verifyTag recomputes the RFC 7518 §5.2.2.1 authentication tag and compares it in constant time.
func verifyTag(macKey, aad, iv, ciphertext, providedTag []byte) error {
	al := make([]byte, 8)
	binary.BigEndian.PutUint64(al, uint64(len(aad))*8)

	mac := hmac.New(sha512.New, macKey)
	mac.Write(aad)
	mac.Write(iv)
	mac.Write(ciphertext)
	mac.Write(al)
	sum := mac.Sum(nil)

	// T_LEN for A256CBC-HS512 is half the HMAC-SHA-512 output: the initial 32 of 64 octets.
	expectedTag := sum[:32]
	if subtle.ConstantTimeCompare(expectedTag, providedTag) != 1 {
		return errors.New("authsession: authentication tag mismatch")
	}
	return nil
}

// aesCBCDecrypt decrypts ciphertext with AES-256-CBC under key/iv and strips PKCS#7 padding.
func aesCBCDecrypt(key, iv, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("ciphertext is not a multiple of the block size")
	}

	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

	padLen := int(plaintext[len(plaintext)-1])
	if padLen == 0 || padLen > aes.BlockSize || padLen > len(plaintext) {
		return nil, errors.New("invalid PKCS#7 padding")
	}
	for _, b := range plaintext[len(plaintext)-padLen:] {
		if int(b) != padLen {
			return nil, errors.New("invalid PKCS#7 padding")
		}
	}
	return plaintext[:len(plaintext)-padLen], nil
}

func base64URLDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// Encode is decode's inverse: it produces a compact JWE an Auth.js `decode` call (or this package's
// own decode) will accept, for the same "dir"/"A256CBC-HS512" scheme session.go documents.
//
// Auth.js itself never needs this — it only ever decodes, since it's the party that already issued
// the cookie via its own `encode`. This exists for two honest reasons: session_test.go's fixture was
// produced by the real @auth/core/jwt library specifically so the decode path is verified against
// the actual thing it must interoperate with, and internal/api's handler tests need a way to
// construct a valid session cookie for a given user without a running Node process. It deliberately
// omits the `kid` header Auth.js includes (a JWK thumbprint) — this package's decode never checks
// it, matching the real library's own behavior when `kid` is absent (see jwt.js: "if (kid ===
// undefined) return encryptionSecret").
func Encode(claims Claims, secret string) (string, error) {
	cookieName := CookieName
	cek, err := derivedEncryptionKey(secret, cookieName)
	if err != nil {
		return "", err
	}
	macKey, encKey := cek[:32], cek[32:]

	header := []byte(`{"alg":"dir","enc":"A256CBC-HS512"}`)
	headerB64 := base64.RawURLEncoding.EncodeToString(header)

	// Auth.js always stamps `exp` from maxAge (30 days by default); the decode path requires it, so
	// tokens minted here must too.
	payload, err := json.Marshal(struct {
		Claims
		Exp int64 `json:"exp"`
	}{Claims: claims, Exp: time.Now().Add(30 * 24 * time.Hour).Unix()})
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(encKey)
	if err != nil {
		return "", err
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}

	padded := pkcs7Pad(payload, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	aad := []byte(headerB64)
	al := make([]byte, 8)
	binary.BigEndian.PutUint64(al, uint64(len(aad))*8)
	mac := hmac.New(sha512.New, macKey)
	mac.Write(aad)
	mac.Write(iv)
	mac.Write(ciphertext)
	mac.Write(al)
	tag := mac.Sum(nil)[:32]

	return strings.Join([]string{
		headerB64,
		"", // "dir" carries no encrypted key segment.
		base64.RawURLEncoding.EncodeToString(iv),
		base64.RawURLEncoding.EncodeToString(ciphertext),
		base64.RawURLEncoding.EncodeToString(tag),
	}, "."), nil
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padLen := blockSize - len(data)%blockSize
	padded := make([]byte, len(data)+padLen)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	return padded
}
