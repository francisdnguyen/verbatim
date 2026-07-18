package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"verbatim/backend/internal/services"
)

// contextKey is a private named type so this package's context values can't
// collide with a key from any other package's use of context.WithValue.
type contextKey string

// userIDKey is where AuthMiddleware stores the authenticated caller's ID.
const userIDKey contextKey = "userID"

// AuthMiddleware requires a valid "Authorization: Bearer <token>" header,
// injecting the token's user ID into the request context for downstream
// handlers. Rejects missing/malformed headers and invalid/expired tokens
// with 401.
func AuthMiddleware(authService *services.AuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if header == "" {
				writeUnauthorized(w, "missing authorization header")
				return
			}

			token, ok := strings.CutPrefix(header, "Bearer ")
			if !ok {
				writeUnauthorized(w, "invalid authorization header format, expected: Bearer <token>")
				return
			}

			userID, err := authService.ValidateToken(token)
			if err != nil {
				writeUnauthorized(w, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), userIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetUserID retrieves the authenticated caller's ID stored by
// AuthMiddleware, returning false if none is present (e.g. an unprotected route).
func GetUserID(r *http.Request) (string, bool) {
	userID, ok := r.Context().Value(userIDKey).(string)
	return userID, ok
}

// writeUnauthorized writes a 401 JSON {"error": message} body. A local
// helper rather than importing package handlers, which would risk a cycle
// and would have the dependency running backwards anyway.
func writeUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
