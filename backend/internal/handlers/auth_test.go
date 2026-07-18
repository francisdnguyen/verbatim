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
	"verbatim/backend/internal/services"
)

// newAuthTestServer wires just the two auth routes behind httptest, mirroring
// main.go's registration (these two routes are deliberately unwrapped by
// AuthMiddleware — they're how a caller gets a token in the first place).
func newAuthTestServer(db *database.DB, authService *services.AuthService) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/register", HandleRegister(db, authService))
	mux.HandleFunc("POST /api/auth/login", HandleLogin(db, authService))
	return httptest.NewServer(mux)
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
	var registerAuth authResponse
	if err := json.NewDecoder(registerResp.Body).Decode(&registerAuth); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if registerAuth.Token == "" {
		t.Error("expected a non-empty token from register")
	}
	if registerAuth.User.Email != email {
		t.Errorf("register user email = %q, want %q", registerAuth.User.Email, email)
	}
	if _, err := authService.ValidateToken(registerAuth.Token); err != nil {
		t.Errorf("expected register's token to validate, got %v", err)
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
	var loginAuth authResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&loginAuth); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if loginAuth.Token == "" {
		t.Error("expected a non-empty token from login")
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
}
