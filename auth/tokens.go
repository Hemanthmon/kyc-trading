package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// newToken creates a signed JWT that says "this is user <id>", valid for tokenTTL.
func (s *Service) newToken(userID string) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
}

// parseToken checks the signature and expiry and returns the user ID inside.
// The token only proves WHO you are; role and KYC status are always read
// fresh from the database, so a revoked user loses access immediately.
func (s *Service) parseToken(tokenStr string) (string, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(tokenStr, &claims,
		func(t *jwt.Token) (any, error) { return s.jwtSecret, nil },
		jwt.WithValidMethods([]string{"HS256"}), // reject "alg":"none" and other tricks
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return "", err
	}
	if claims.Subject == "" {
		return "", errors.New("token has no subject")
	}
	return claims.Subject, nil
}
