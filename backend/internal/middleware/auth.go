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

// csrfTokenKey is where AuthMiddleware stores the request's verified CSRF
// token — needed so HandleMe can hand it back to the frontend on a page
// reload, the only other place besides login/register that the frontend
// can (re-)learn its in-memory CSRF value from.
const csrfTokenKey contextKey = "csrfToken"

// TokenCookieName holds the signed JWT, set httpOnly so client-side JS can
// never read it (closes the XSS-token-exfiltration path localStorage had).
// Exported so handlers.go (which sets/clears it on login/register/logout)
// and this middleware (which reads it) share one name.
const TokenCookieName = "verbatim_token"

// AuthMiddleware requires a valid session cookie (see TokenCookieName),
// injecting the token's user ID into the request context for downstream
// handlers. For any state-changing method (everything but GET/HEAD/OPTIONS)
// it also requires the X-CSRF-Token header to match the CSRF value signed
// into the token itself (services.TokenClaims.CSRFToken) — the session
// cookie alone isn't proof of intent, since a browser attaches cookies to
// cross-site requests automatically regardless of who triggered them. This
// is a double-submit pattern with the "cookie" half folded into the JWT
// rather than a second real cookie — see the comment on
// services.AuthService.ValidateToken for why: frontend and backend sit on
// different domains, so a plain second cookie set by the backend would
// never be readable by the frontend's own JS to echo back.
func AuthMiddleware(authService *services.AuthService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(TokenCookieName)
			if err != nil || cookie.Value == "" {
				writeUnauthorized(w, "missing or invalid session")
				return
			}

			claims, err := authService.ValidateToken(cookie.Value)
			if err != nil {
				writeUnauthorized(w, "invalid or expired session")
				return
			}

			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				// A plain != is intentional, not an oversight: this isn't a
				// second comparison against a server-side secret — the JWT
				// signature above already proved claims.CSRFToken is
				// genuine, so this is just checking the caller can produce
				// the value that was returned in the login/register/me
				// response body. No timing side-channel worth defending
				// with subtle.ConstantTimeCompare.
				if claims.CSRFToken == "" || claims.CSRFToken != r.Header.Get("X-CSRF-Token") {
					writeForbidden(w, "missing or invalid CSRF token")
					return
				}
			}

			ctx := context.WithValue(r.Context(), userIDKey, claims.UserID)
			ctx = context.WithValue(ctx, csrfTokenKey, claims.CSRFToken)
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

// GetCSRFToken retrieves the authenticated caller's CSRF token stored by
// AuthMiddleware — HandleMe uses this to hand the value back to the
// frontend on a page reload.
func GetCSRFToken(r *http.Request) (string, bool) {
	token, ok := r.Context().Value(csrfTokenKey).(string)
	return token, ok
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
