package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/joho/godotenv"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/middleware"
	"verbatim/backend/internal/services"
)

// newAuthTestServer wires the auth routes behind httptest, mirroring main.go's
// registration (these are deliberately unwrapped by AuthMiddleware — they're
// how a caller gets a session in the first place). secureCookies is always
// false here since httptest serves plain HTTP, same as local dev.
func newAuthTestServer(db *database.DB, authService *services.AuthService) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/register", HandleRegister(db, authService, false))
	mux.HandleFunc("POST /api/auth/login", HandleLogin(db, authService, false))
	mux.HandleFunc("POST /api/auth/logout", HandleLogout(false))
	mux.Handle("GET /api/auth/me", middleware.AuthMiddleware(authService)(HandleMe(db)))
	return httptest.NewServer(mux)
}

// sessionCookie extracts the session cookie from a register/login response,
// failing the test if it's missing. The CSRF token isn't a cookie at all —
// it comes back in the response body (userResponse.CSRFToken) instead, see
// the comment on services.AuthService.ValidateToken for why.
func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == middleware.TokenCookieName {
			return c
		}
	}
	t.Fatal("expected a session cookie to be set")
	return nil
}

// TestAuthHandlers_Live drives register/login over real HTTP against a real
// local Postgres, matching this codebase's existing live-test gating pattern.
func TestAuthHandlers_Live(t *testing.T) {
	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL must be set; skipping live auth handler test")
	}

	ctx := context.Background()
	db, err := database.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()

	authService := services.NewAuthService(testJWTSecret)
	srv := newAuthTestServer(db, authService)
	defer srv.Close()

	// Registering a brand-new email succeeds with 201 and a usable token.
	email := "auth-handlers-test@example.com"
	defer db.Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email)

	registerBody, _ := json.Marshal(map[string]string{"email": email, "password": "correct-horse-battery-staple"})
	registerResp, err := http.Post(srv.URL+"/api/auth/register", "application/json", bytes.NewReader(registerBody))
	if err != nil {
		t.Fatalf("POST /api/auth/register: %v", err)
	}
	defer registerResp.Body.Close()
	if registerResp.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, want %d", registerResp.StatusCode, http.StatusCreated)
	}
	var registerAuth userResponse
	if err := json.NewDecoder(registerResp.Body).Decode(&registerAuth); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if registerAuth.User.Email != email {
		t.Errorf("register user email = %q, want %q", registerAuth.User.Email, email)
	}
	if registerAuth.CSRFToken == "" {
		t.Error("expected a non-empty CSRF token in the register response body")
	}
	regSession := sessionCookie(t, registerResp)
	if !regSession.HttpOnly {
		t.Error("expected the session cookie to be HttpOnly")
	}
	claims, err := authService.ValidateToken(regSession.Value)
	if err != nil {
		t.Errorf("expected register's session cookie to validate, got %v", err)
	}
	if claims.CSRFToken != registerAuth.CSRFToken {
		t.Errorf("session cookie's embedded CSRF claim = %q, want it to match the response body's %q", claims.CSRFToken, registerAuth.CSRFToken)
	}

	// Registering the same email again is a 409, not a silent success.
	dupResp, err := http.Post(srv.URL+"/api/auth/register", "application/json", bytes.NewReader(registerBody))
	if err != nil {
		t.Fatalf("POST duplicate register: %v", err)
	}
	dupResp.Body.Close()
	if dupResp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate register status = %d, want %d", dupResp.StatusCode, http.StatusConflict)
	}

	// Logging in with the correct password succeeds with 200 and a usable token.
	loginBody, _ := json.Marshal(map[string]string{"email": email, "password": "correct-horse-battery-staple"})
	loginResp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("POST /api/auth/login: %v", err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want %d", loginResp.StatusCode, http.StatusOK)
	}
	var loginAuth userResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&loginAuth); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if loginAuth.CSRFToken == "" {
		t.Error("expected a non-empty CSRF token in the login response body")
	}
	loginSession := sessionCookie(t, loginResp)
	if _, err := authService.ValidateToken(loginSession.Value); err != nil {
		t.Errorf("expected login's session cookie to validate, got %v", err)
	}

	// Wrong password: 401.
	wrongPasswordBody, _ := json.Marshal(map[string]string{"email": email, "password": "not-the-password"})
	wrongPasswordResp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewReader(wrongPasswordBody))
	if err != nil {
		t.Fatalf("POST login wrong password: %v", err)
	}
	wrongPasswordResp.Body.Close()
	if wrongPasswordResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password status = %d, want %d", wrongPasswordResp.StatusCode, http.StatusUnauthorized)
	}

	// Nonexistent email: also 401, same as a wrong password (no email enumeration).
	noSuchUserBody, _ := json.Marshal(map[string]string{"email": "no-such-user@example.com", "password": "whatever"})
	noSuchUserResp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewReader(noSuchUserBody))
	if err != nil {
		t.Fatalf("POST login nonexistent user: %v", err)
	}
	noSuchUserResp.Body.Close()
	if noSuchUserResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("nonexistent user status = %d, want %d", noSuchUserResp.StatusCode, http.StatusUnauthorized)
	}

	// Short password: 400.
	shortPasswordBody, _ := json.Marshal(map[string]string{"email": "another-new-user@example.com", "password": "short"})
	shortPasswordResp, err := http.Post(srv.URL+"/api/auth/register", "application/json", bytes.NewReader(shortPasswordBody))
	if err != nil {
		t.Fatalf("POST register short password: %v", err)
	}
	shortPasswordResp.Body.Close()
	if shortPasswordResp.StatusCode != http.StatusBadRequest {
		t.Errorf("short password status = %d, want %d", shortPasswordResp.StatusCode, http.StatusBadRequest)
	}

	// /api/auth/me: the session cookie alone (no CSRF header needed — it's a
	// GET) identifies the caller and hands back a fresh CSRF token for the
	// frontend to re-learn on a page reload.
	meReq, _ := http.NewRequest("GET", srv.URL+"/api/auth/me", nil)
	meReq.AddCookie(regSession)
	meResp, err := http.DefaultClient.Do(meReq)
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("me status = %d, want %d", meResp.StatusCode, http.StatusOK)
	}
	var me userResponse
	if err := json.NewDecoder(meResp.Body).Decode(&me); err != nil {
		t.Fatalf("decode me response: %v", err)
	}
	if me.User.Email != email {
		t.Errorf("me user email = %q, want %q", me.User.Email, email)
	}
	if me.CSRFToken != registerAuth.CSRFToken {
		t.Errorf("me CSRFToken = %q, want it to match the original session's %q", me.CSRFToken, registerAuth.CSRFToken)
	}

	// /api/auth/me with no cookie at all: 401.
	noCookieResp, err := http.Get(srv.URL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me without cookie: %v", err)
	}
	noCookieResp.Body.Close()
	if noCookieResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("me without cookie status = %d, want %d", noCookieResp.StatusCode, http.StatusUnauthorized)
	}

	// /api/auth/logout clears the session cookie (Max-Age < 0 tells the
	// browser to delete it immediately).
	logoutResp, err := http.Post(srv.URL+"/api/auth/logout", "", nil)
	if err != nil {
		t.Fatalf("POST /api/auth/logout: %v", err)
	}
	defer logoutResp.Body.Close()
	if logoutResp.StatusCode != http.StatusNoContent {
		t.Errorf("logout status = %d, want %d", logoutResp.StatusCode, http.StatusNoContent)
	}
	cleared := false
	for _, c := range logoutResp.Cookies() {
		if c.Name == middleware.TokenCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("expected logout to clear the session cookie")
	}
}

// TestAuthHandlers_Live_MeWithDeletedUser is a regression test for a bug
// karen caught by actually minting a token for a nonexistent user and
// hitting the live endpoint: HandleMe didn't check for
// database.ErrUserNotFound the way every other handler in this file does,
// so a validly-signed session cookie for a user deleted after the token was
// issued returned a raw 500 instead of a 401 — which the frontend's
// getCurrentUser() (only special-cases 401 as "logged out") would have
// thrown on instead of gracefully falling back to the logged-out view.
func TestAuthHandlers_Live_MeWithDeletedUser(t *testing.T) {
	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL must be set; skipping live auth handler test")
	}

	ctx := context.Background()
	db, err := database.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()

	authService := services.NewAuthService(testJWTSecret)
	srv := newAuthTestServer(db, authService)
	defer srv.Close()

	// A syntactically valid UUID with no backing row — same shape a real
	// token has, just for a user that (say) got deleted after the token was
	// issued.
	token, err := authService.GenerateToken("00000000-0000-0000-0000-000000000099", "ghost@example.com", "csrf-ghost")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: middleware.TokenCookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("me for deleted user status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}
