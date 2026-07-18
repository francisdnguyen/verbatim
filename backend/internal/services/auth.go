package services

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidToken is returned when a token fails to parse, fails signature
// verification, uses an unexpected signing algorithm, or has expired.
var ErrInvalidToken = errors.New("invalid or expired token")

// tokenTTL is how long an issued token remains valid. No refresh mechanism
// exists yet, so this is a hard re-login boundary, not a renewable session.
const tokenTTL = 24 * time.Hour

// authClaims is the JWT payload: the caller's user ID and email alongside
// the standard registered claims (expiry).
type authClaims struct {
	UserID string `json:"sub"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

// AuthService hashes/verifies passwords and issues/validates JWTs, holding
// the signing secret alongside its methods — matching the constructor+
// methods shape already used by Client/OpenAIClient/S3Client.
type AuthService struct {
	jwtSecret []byte
}

// NewAuthService builds an AuthService from its signing secret.
func NewAuthService(jwtSecret string) *AuthService {
	return &AuthService{jwtSecret: []byte(jwtSecret)}
}

// HashPassword returns a bcrypt hash of password, suitable for storage in
// users.password_hash.
func (s *AuthService) HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// CheckPassword reports whether password matches hash. Callers only need a
// yes/no, so bcrypt's own error type is collapsed to a bool here.
func (s *AuthService) CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// GenerateToken issues a signed JWT for userID/email, valid for tokenTTL.
func (s *AuthService) GenerateToken(userID, email string) (string, error) {
	claims := authClaims{
		UserID: userID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(tokenTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return signed, nil
}

// ValidateToken parses and verifies tokenString, returning the user ID it
// was issued for. Rejects anything not signed with HMAC (guards against the
// classic "alg: none" / algorithm-confusion JWT pitfall) as ErrInvalidToken,
// same as any other parse/signature/expiry failure.
func (s *AuthService) ValidateToken(tokenString string) (string, error) {
	claims := &authClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return s.jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return "", ErrInvalidToken
	}
	return claims.UserID, nil
}
