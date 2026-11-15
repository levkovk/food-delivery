package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const secret = "0123456789abcdef0123456789abcdef"

func TestIssueAndVerify(t *testing.T) {
	iss, err := NewIssuer(secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := iss.Issue("operator", RoleOperator, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c, err := iss.Verify(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.Subject != "operator" || c.Role != RoleOperator {
		t.Fatalf("unexpected claims %+v", c)
	}
}

func TestVerifyRejectsExpiredAndForeign(t *testing.T) {
	iss, _ := NewIssuer(secret, time.Minute)
	old, _ := iss.Issue("admin", RoleAdmin, time.Now().Add(-2*time.Hour))
	if _, err := iss.Verify(old); err == nil {
		t.Fatal("expired token accepted")
	}
	other, _ := NewIssuer(strings.Repeat("x", 32), time.Hour)
	foreign, _ := other.Issue("admin", RoleAdmin, time.Now())
	if _, err := iss.Verify(foreign); err == nil {
		t.Fatal("token signed with another key accepted")
	}
}

func TestVerifyRejectsNoneAlgorithmAndUnknownRole(t *testing.T) {
	iss, _ := NewIssuer(secret, time.Hour)
	claims := Claims{Role: RoleAdmin, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "food-delivery", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := iss.Verify(none); err == nil {
		t.Fatal("alg=none accepted")
	}
	claims.Role = "root"
	bad, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if _, err := iss.Verify(bad); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestShortSecretRejected(t *testing.T) {
	if _, err := NewIssuer("short", time.Hour); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$") {
		t.Fatalf("unexpected format %q", h)
	}
	if !CheckPassword("s3cret-pass", h) {
		t.Fatal("correct password rejected")
	}
	if CheckPassword("wrong", h) || CheckPassword("s3cret-pass", "garbage") {
		t.Fatal("wrong password accepted")
	}
	h2, _ := HashPassword("s3cret-pass")
	if h == h2 {
		t.Fatal("salt is not random")
	}
}
