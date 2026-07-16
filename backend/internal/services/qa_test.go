package services

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/joho/godotenv"

	"verbatim/backend/internal/database"
)

// TestAnswerQuestion_Live ingests a real video, then asks a real question
// about it and confirms a grounded answer with real sources comes back.
func TestAnswerQuestion_Live(t *testing.T) {
	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	supadataKey := os.Getenv("SUPADATA_API_KEY")
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if databaseURL == "" || supadataKey == "" || openaiKey == "" {
		t.Skip("DATABASE_URL, SUPADATA_API_KEY, and OPENAI_API_KEY must all be set; skipping live Q&A test")
	}

	ctx := context.Background()

	db, err := database.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()

	var userID string
	err = db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2)
		 ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id`,
		"qa-test@example.com", "test-hash",
	).Scan(&userID)
	if err != nil {
		t.Fatalf("upsert test user: %v", err)
	}

	transcriptClient := NewClient(supadataKey)
	embedClient := NewEmbeddingClient(openaiKey)

	video, err := IngestYouTubeVideo(ctx, db, transcriptClient, embedClient, userID, liveTestVideo, "en")
	if err != nil {
		t.Fatalf("IngestYouTubeVideo: %v", err)
	}
	defer db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, video.ID)

	answer, err := AnswerQuestion(ctx, db, embedClient, video.ID, "What is this video about?")
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if answer.Text == "" {
		t.Error("expected a non-empty answer")
	}
	if len(answer.Sources) == 0 {
		t.Fatal("expected at least one source")
	}
	for _, s := range answer.Sources {
		if s.Text == "" {
			t.Error("expected source text to be non-empty")
		}
		if s.EndSeconds < s.StartSeconds {
			t.Errorf("source end (%v) before start (%v)", s.EndSeconds, s.StartSeconds)
		}
	}
	t.Logf("answer: %s", answer.Text)
}

// TestAnswerQuestion_Live_NotReady confirms asking about a video that hasn't
// finished processing yet returns ErrVideoNotReady rather than an answer.
func TestAnswerQuestion_Live_NotReady(t *testing.T) {
	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	supadataKey := os.Getenv("SUPADATA_API_KEY")
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if databaseURL == "" || supadataKey == "" || openaiKey == "" {
		t.Skip("DATABASE_URL, SUPADATA_API_KEY, and OPENAI_API_KEY must all be set; skipping live Q&A test")
	}

	ctx := context.Background()

	db, err := database.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer db.Close()

	var userID string
	err = db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2)
		 ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id`,
		"qa-test@example.com", "test-hash",
	).Scan(&userID)
	if err != nil {
		t.Fatalf("upsert test user: %v", err)
	}

	video, err := db.CreateVideo(ctx, userID, liveTestVideo)
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}
	defer db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, video.ID)

	embedClient := NewEmbeddingClient(openaiKey)

	_, err = AnswerQuestion(ctx, db, embedClient, video.ID, "What is this video about?")
	if err == nil {
		t.Fatal("expected an error for a not-yet-ready video, got nil")
	}
	if !errors.Is(err, ErrVideoNotReady) {
		t.Errorf("err = %v, want ErrVideoNotReady", err)
	}
}
