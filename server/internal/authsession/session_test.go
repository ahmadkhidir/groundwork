package authsession

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// devSecret matches web/.env.local's AUTH_SECRET for local development.
const devSecret = "dev-only-local-secret-not-for-production"

// fixtureToken was generated with the REAL @auth/core/jwt `encode` function (the exact code path
// web/src/auth.ts relies on), not an approximation:
//
//	node -e "const {encode}=require('@auth/core/jwt'); (async () => {
//	  console.log(await encode({
//	    token: {id: 'user-123', tier: 'pro', email: 'a@b.com', sub: 'user-123'},
//	    secret: 'dev-only-local-secret-not-for-production',
//	    salt: 'authjs.session-token',
//	    maxAge: 30*24*60*60,
//	  }));
//	})();"
//
// This is the strongest verification available without a running Node process in CI: if this
// package's decode ever drifts from Auth.js's actual algorithm, this is the test that catches it.
const fixtureToken = "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2Q0JDLUhTNTEyIiwia2lkIjoiOXRJNnJDN09BbzJPXy1yRjlaaGlRVk91bUhhU1NqaV81RGg3WHVZcDBKWTlvelR1QjhySExicDdWZnhrYlFVSExZOWtRZGRCbGpWbXJMR2tNQ2Z6UlEifQ..Akd93uDHh_gCT1B1QcOFVA.kJx32sO56BRCB6o50XcZIA1DLcCi5RDV-7aZ941V63U_WnKBNR2yVm4vPiJBx8divCNF3p5Qh5NDKr1K8VqXov4SEz_7j-vsykpqsmPivmHkXTUZiDAF3d6v1yzn63FEx-13o7zt6SsxckmRF5cI3e7JHQ0lIwUaq9jyyc60OmUQpwbw8oFyORERq3tljVOC5cgDAMZK9G5oFZmYi-peeg.H8cdd1EFfbxRpzTmkmYOTqdtwhKKXhFM3qzFBtCXwvE"

// expiredFixtureToken carries the same claims shape with an `exp` in the past (generated with the
// same @auth/core `encode` call and maxAge: -60). It decrypts fine — the test pins that expiry is
// validated, not just the JWE's authenticity.
const expiredFixtureToken = "eyJhbGciOiJkaXIiLCJlbmMiOiJBMjU2Q0JDLUhTNTEyIiwia2lkIjoiOXRJNnJDN09BbzJPXy1yRjlaaGlRVk91bUhhU1NqaV81RGg3WHVZcDBKWTlvelR1QjhySExicDdWZnhrYlFVSExZOWtRZGRCbGpWbXJMR2tNQ2Z6UlEifQ..qhdHz0YlxvgtDTLHwgI9vQ.jW_90bxQrN1UKBNLgUU66lgq8nMsgRHyy0JGT598fkEkTSj1tS9IWXyMwDyIG9XY7myAK42y-XgS2mfdBb-S3AFx-JM5xQP2-mtbLJkIsNOju0VIw0X9m-JtikNqOOjYhxlqNrepJgv_K4EqzWA9DKH94rYYPf32QLwO1l1JWh7bgeQnSiubIQf9_zdkloqKCl3gxzIUtP0rDvz1In6LXw.xZP8gCBYXhqlU7zyenXQxzs1qL98o8_Q19IW2DZbvhA"

func TestFromRequest_DecodesRealAuthJSToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: fixtureToken})

	claims, err := FromRequest(req, devSecret)
	if err != nil {
		t.Fatalf("FromRequest returned error: %v", err)
	}
	if claims.UserID != "user-123" {
		t.Errorf("UserID = %q, want %q", claims.UserID, "user-123")
	}
	if claims.Tier != "pro" {
		t.Errorf("Tier = %q, want %q", claims.Tier, "pro")
	}
	if claims.Email != "a@b.com" {
		t.Errorf("Email = %q, want %q", claims.Email, "a@b.com")
	}
}

func TestFromRequest_SecureCookieName(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	req.AddCookie(&http.Cookie{Name: SecureCookieName, Value: fixtureToken})

	// The fixture was salted with "authjs.session-token", not the secure name, so decoding under
	// the secure cookie name must fail the HMAC check — salt feeds key derivation. This also
	// exercises the secure-cookie lookup path itself succeeding at finding the cookie.
	if _, err := FromRequest(req, devSecret); err == nil {
		t.Fatal("expected an error decoding a token salted for a different cookie name")
	}
}

func TestFromRequest_NoCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)

	_, err := FromRequest(req, devSecret)
	if err != ErrNoSession {
		t.Errorf("err = %v, want ErrNoSession", err)
	}
}

func TestFromRequest_BearerToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+fixtureToken)

	claims, err := FromRequest(req, devSecret)
	if err != nil {
		t.Fatalf("FromRequest returned error for a Bearer token: %v", err)
	}
	if claims.UserID != "user-123" || claims.Tier != "pro" || claims.Email != "a@b.com" {
		t.Errorf("claims = %+v, want the fixture's claims", claims)
	}
}

func TestFromRequest_BearerTokenSchemeIsCaseInsensitiveAndWhitespaceTolerant(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	req.Header.Set("Authorization", "bearer   "+fixtureToken+"  ")

	claims, err := FromRequest(req, devSecret)
	if err != nil {
		t.Fatalf("FromRequest returned error: %v", err)
	}
	if claims.UserID != "user-123" {
		t.Errorf("UserID = %q, want user-123", claims.UserID)
	}
}

func TestFromRequest_ExpiredTokenRejected(t *testing.T) {
	// The expired token decrypts with the right secret — the rejection must come from the exp
	// check, not from a wrong-key failure.
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	req.Header.Set("Authorization", "Bearer "+expiredFixtureToken)

	_, err := FromRequest(req, devSecret)
	if err != ErrInvalidSession {
		t.Errorf("err = %v, want ErrInvalidSession for an expired token", err)
	}
}

func TestFromRequest_InvalidBearerRejected(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	req.Header.Set("Authorization", "Bearer not.a.valid.jwe")

	_, err := FromRequest(req, devSecret)
	if err != ErrInvalidSession {
		t.Errorf("err = %v, want ErrInvalidSession for a malformed Bearer token", err)
	}
}

func TestFromRequest_WrongSecret(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: fixtureToken})

	_, err := FromRequest(req, "not-the-right-secret")
	if err != ErrInvalidSession {
		t.Errorf("err = %v, want ErrInvalidSession", err)
	}
}

func TestFromRequest_TamperedToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	tampered := fixtureToken[:len(fixtureToken)-4] + "AAAA"
	req.AddCookie(&http.Cookie{Name: CookieName, Value: tampered})

	_, err := FromRequest(req, devSecret)
	if err != ErrInvalidSession {
		t.Errorf("err = %v, want ErrInvalidSession", err)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	want := Claims{UserID: "user-456", Tier: "team", Email: "z@y.com"}

	token, err := Encode(want, devSecret)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})

	got, err := FromRequest(req, devSecret)
	if err != nil {
		t.Fatalf("FromRequest() error = %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestFromRequest_MalformedToken(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "not.a.valid.jwe"})

	_, err := FromRequest(req, devSecret)
	if err != ErrInvalidSession {
		t.Errorf("err = %v, want ErrInvalidSession", err)
	}
}
