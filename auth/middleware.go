package auth

import (
	"context"
	"kyc-backend/models"
	"net/http"
	"slices"
	"strings"
)

type ctxKey struct{}

// UserFrom returns the logged-in user that RequireAuth put into the request.
func UserFrom(ctx context.Context) *models.User {
	u, _ := ctx.Value(ctxKey{}).(*models.User)
	return u
}

// RequireAuth lets the request through only with a valid token.
// The token comes from "Authorization: Bearer <token>", or from ?token=<token>
// because browsers cannot set headers on a WebSocket connection.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenStr := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tokenStr == "" || tokenStr == r.Header.Get("Authorization") {
			tokenStr = r.URL.Query().Get("token")
		}
		if tokenStr == "" {
			writeError(w, http.StatusUnauthorized, "missing token")
			return
		}

		userID, err := s.parseToken(tokenStr)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}

		u, err := s.userByID(r.Context(), userID) // fresh from DB: role/KYC changes apply at once
		if err != nil {
			writeError(w, http.StatusUnauthorized, "user not found")
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

// RequireKYC lets only KYC-verified users through. Use it after RequireAuth.
// This is the gate in front of trading functionality (live prices).
func RequireKYC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := UserFrom(r.Context())
		if u == nil || u.KYC.Status != models.KYCVerified {
			writeError(w, http.StatusForbidden, "KYC verification required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireRole lets only users with one of the given roles through. Use it after RequireAuth.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := UserFrom(r.Context())
			if u == nil || !slices.Contains(roles, u.Role) {
				writeError(w, http.StatusForbidden, "not allowed for your role")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
