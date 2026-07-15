package services

import (
	"context"
	"os"
	"testing"

	"github.com/joho/godotenv"
	pgvector "github.com/pgvector/pgvector-go"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/models"
)

// TestIngestYouTubeVideo_Live runs the full transcript -> chunk -> embed ->
// store pipeline against real services. Skips unless DATABASE_URL,
// SUPADATA_API_KEY, and OPENAI_API_KEY are all set, matching the gating
// pattern of the other live tests in this package.
func TestIngestYouTubeVideo_Live(t *testing.T) {
	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	supadataKey := os.Getenv("SUPADATA_API_KEY")
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if databaseURL == "" || supadataKey == "" || openaiKey == "" {
		t.Skip("DATABASE_URL, SUPADATA_API_KEY, and OPENAI_API_KEY must all be set; skipping live pipeline test")
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
		"ingest-test@example.com", "test-hash",
	).Scan(&userID)
	if err != nil {
		t.Fatalf("upsert test user: %v", err)
	}

	transcriptClient := NewClient(supadataKey)
	embedClient := NewEmbeddingClient(openaiKey)

	// liveTestVideo is the same fixture URL used by the Supadata/embed live tests.
	video, err := IngestYouTubeVideo(ctx, db, transcriptClient, embedClient, userID, liveTestVideo, "en")
	if err != nil {
		t.Fatalf("IngestYouTubeVideo: %v", err)
	}
	defer db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, video.ID)

	if video.Status != models.VideoStatusReady {
		t.Errorf("Status = %q, want %q", video.Status, models.VideoStatusReady)
	}

	var chunkCount int
	err = db.QueryRow(ctx, `SELECT COUNT(*) FROM chunks WHERE video_id = $1`, video.ID).Scan(&chunkCount)
	if err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if chunkCount == 0 {
		t.Fatal("expected at least one chunk to be stored")
	}
	t.Logf("ingested %d chunks", chunkCount)

	var vec pgvector.Vector
	err = db.QueryRow(ctx,
		`SELECT embedding FROM chunks WHERE video_id = $1 ORDER BY chunk_index LIMIT 1`, video.ID,
	).Scan(&vec)
	if err != nil {
		t.Fatalf("query first chunk embedding: %v", err)
	}
	if len(vec.Slice()) != 1536 {
		t.Errorf("embedding dims = %d, want 1536", len(vec.Slice()))
	}
}

// TestIngestYouTubeVideo_Live_FailurePathMarksVideoFailed forces a failure
// after the video row already exists (a nonexistent video ID makes
// FetchTranscript fail) and confirms the row actually ends up 'failed'
// rather than stuck at 'processing' forever. Regression test: an earlier
// version returned a zero-valued Video on every error path, which silently
// broke the defer-based failure-marking (it tried to update status for an
// empty video ID and never touched the real row) — caught by the review
// process, not by this test's absence alone, but this closes the gap.
func TestIngestYouTubeVideo_Live_FailurePathMarksVideoFailed(t *testing.T) {
	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	supadataKey := os.Getenv("SUPADATA_API_KEY")
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if databaseURL == "" || supadataKey == "" || openaiKey == "" {
		t.Skip("DATABASE_URL, SUPADATA_API_KEY, and OPENAI_API_KEY must all be set; skipping live pipeline test")
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
		"ingest-test@example.com", "test-hash",
	).Scan(&userID)
	if err != nil {
		t.Fatalf("upsert test user: %v", err)
	}

	transcriptClient := NewClient(supadataKey)
	embedClient := NewEmbeddingClient(openaiKey)

	// A syntactically valid but nonexistent YouTube video ID: CreateVideo
	// succeeds (so the failure-marking defer is registered), but
	// FetchTranscript fails, exercising the failure path end to end.
	video, err := IngestYouTubeVideo(ctx, db, transcriptClient, embedClient, userID, "https://www.youtube.com/watch?v=0000000000X", "en")
	if video.ID != "" {
		defer db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, video.ID)
	}

	if err == nil {
		t.Fatal("expected an error for a nonexistent video, got nil")
	}
	if video.ID == "" {
		t.Fatal("expected the returned Video to still carry its real ID on failure, got empty ID")
	}

	var status string
	err = db.QueryRow(ctx, `SELECT status FROM videos WHERE id = $1`, video.ID).Scan(&status)
	if err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != string(models.VideoStatusFailed) {
		t.Errorf("status = %q, want %q — video should be marked failed, not stuck at an earlier status", status, models.VideoStatusFailed)
	}
}
