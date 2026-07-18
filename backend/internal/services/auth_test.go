package services

import "testing"

func TestAuthService_HashAndCheckPassword(t *testing.T) {
	s := NewAuthService("test-secret")

	hash, err := s.HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "correct-horse-battery-staple" {
		t.Fatal("expected the hash to differ from the plaintext password")
	}

	if !s.CheckPassword(hash, "correct-horse-battery-staple") {
		t.Error("expected CheckPassword to accept the correct password")
	}
	if s.CheckPassword(hash, "wrong-password") {
		t.Error("expected CheckPassword to reject an incorrect password")
	}
}

func TestAuthService_GenerateAndValidateToken(t *testing.T) {
	s := NewAuthService("test-secret")

	token, err := s.GenerateToken("user-123", "user@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	userID, err := s.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if userID != "user-123" {
		t.Errorf("ValidateToken userID = %q, want %q", userID, "user-123")
	}
}

func TestAuthService_ValidateToken_RejectsGarbage(t *testing.T) {
	s := NewAuthService("test-secret")

	if _, err := s.ValidateToken("not-a-real-token"); err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken for a malformed token, got %v", err)
	}
}

func TestAuthService_ValidateToken_RejectsWrongSecret(t *testing.T) {
	issuer := NewAuthService("secret-a")
	verifier := NewAuthService("secret-b")

	token, err := issuer.GenerateToken("user-123", "user@example.com")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	if _, err := verifier.ValidateToken(token); err != ErrInvalidToken {
		t.Errorf("expected ErrInvalidToken for a token signed with a different secret, got %v", err)
	}
}
