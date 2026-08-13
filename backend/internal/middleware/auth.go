package middleware

import (
	"context"
	"encoding/json"
	"net/http"

	"verbatim/backend/internal/services"
)

// contextKey is a private named type so this package's context values can't
// collide with a key from any other package's use of context.WithValue.
type contextKey string

// userIDKey is where AuthMiddleware stores the authenticated caller's ID.
const userIDKey contextKey = "userID"

// TokenCookieName holds the signed JWT, set httpOnly so client-side JS can
// never read it (closes the XSS-token-exfiltration path localStorage had).
// Exported so handlers.go (which sets/clears it on login/register/logout)
// and this middleware (which reads it) share one name.
const TokenCookieName = "verbatim_token"

// CSRFCookieName holds the double-submit CSRF token. Deliberately NOT
// httpOnly — the frontend must be able to read it and echo it back in the
// X-CSRF-Token header, which is exactly what proves the request came from
// same-origin JS and not a cross-site form/script riding the auth cookie.
const CSRFCookieName = "verbatim_csrf"

// AuthMiddleware requires a valid session cookie (see TokenCookieName),
// injecting the token's user ID into the request context for downstream
// handlers. For any state-changing method (everything but GET/HEAD/OPTIONS)
// it also requires the CSRF cookie's value to match the X-CSRF-Token header
// — the cookie alone isn't enough proof of intent, since a browser attaches
// cookies to cross-site requests automatically regardless of who triggered
// them.
func AuthMiddleware(authService *services.AuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(TokenCookieName)
			if err != nil || cookie.Value == "" {
				writeUnauthorized(w, "missing or invalid session")
				return
			}

			userID, err := authService.ValidateToken(cookie.Value)
			if err != nil {
				writeUnauthorized(w, "invalid or expired session")
				return
			}

			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				// A plain != is intentional, not an oversight: unlike the JWT
				// signature above, this isn't comparing against a server-side
				// secret — both the cookie and the header are values the
				// browser/attacker can already observe, so there's no timing
				// side-channel worth defending with subtle.ConstantTimeCompare.
				csrfCookie, err := r.Cookie(CSRFCookieName)
				if err != nil || csrfCookie.Value == "" || csrfCookie.Value != r.Header.Get("X-CSRF-Token") {
					writeForbidden(w, "missing or invalid CSRF token")
					return
				}
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

// writeForbidden writes a 403 JSON {"error": message} body — distinct from
// writeUnauthorized: the caller has a valid session, but the request itself
// failed the CSRF check.
func writeForbidden(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
