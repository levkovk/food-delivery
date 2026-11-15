// Package auth содержит выпуск и проверку JWT и хеширование паролей.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Роли пользователей системы.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
)

const iterations = 210000

// Claims - полезная нагрузка токена доступа.
type Claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// Issuer выпускает и проверяет токены HS256.
type Issuer struct {
	secret []byte
	ttl    time.Duration
}

// NewIssuer создаёт выпускающего с ключом подписи и сроком жизни токена.
func NewIssuer(secret string, ttl time.Duration) (*Issuer, error) {
	if len(secret) < 32 {
		return nil, errors.New("jwt secret must be at least 32 bytes")
	}
	return &Issuer{secret: []byte(secret), ttl: ttl}, nil
}

// Issue выпускает токен для пользователя.
func (i *Issuer) Issue(user, role string, now time.Time) (string, error) {
	claims := Claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user,
			Issuer:    "food-delivery",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.secret)
}

// Verify проверяет подпись, алгоритм и срок действия токена.
func (i *Issuer) Verify(token string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		return i.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("food-delivery"), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	if claims.Role != RoleAdmin && claims.Role != RoleOperator {
		return nil, errors.New("unknown role")
	}
	return claims, nil
}

// HashPassword возвращает строку вида pbkdf2-sha256$итерации$соль$хеш.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// CheckPassword сравнивает пароль с сохранённым хешем за постоянное время.
func CheckPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1000 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}
