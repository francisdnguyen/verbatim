package database

import (
	"context"
	"os"
	"testing"

	"github.com/joho/godotenv"

	"verbatim/backend/internal/models"
)

// testDB connects to the local Postgres and ensures a throwaway test user
// exists (same pattern cmd/api/main.go's smoke test uses), returning the DB
// and that user's ID. Skips the test if DATABASE_URL isn't configured.
func testDB(t *testing.T) (*DB, string) {
	t.Helper()
	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set; skipping live DB test")
	}

	ctx := context.Background()
	db, err := Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	var userID string
	err = db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2)
		 ON CONFLICT (email) DO UPDATE SET email = EXCLUDED.email
		 RETURNING id`,
		"db-test@example.com", "test-hash",
	).Scan(&userID)
	if err != nil {
		t.Fatalf("upsert test user: %v", err)
	}

	return db, userID
}

func TestCreateVideo_Live(t *testing.T) {
	db, userID := testDB(t)
	ctx := context.Background()

	video, err := db.CreateVideo(ctx, userID, "https://www.youtube.com/watch?v=jNQXAC9IVRw")
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}
	t.Cleanup(func() { db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, video.ID) })

	if video.ID == "" {
		t.Error("expected a generated ID")
	}
	if video.UserID != userID {
		t.Errorf("UserID = %q, want %q", video.UserID, userID)
	}
	if video.SourceType != models.VideoSourceYouTube {
		t.Errorf("SourceType = %q, want %q", video.SourceType, models.VideoSourceYouTube)
	}
	if video.SourceURL == nil || *video.SourceURL != "https://www.youtube.com/watch?v=jNQXAC9IVRw" {
		t.Errorf("SourceURL = %v, want the submitted URL", video.SourceURL)
	}
	if video.Status != models.VideoStatusPending {
		t.Errorf("Status = %q, want %q", video.Status, models.VideoStatusPending)
	}
}

func TestUpdateVideoStatus_Live(t *testing.T) {
	db, userID := testDB(t)
	ctx := context.Background()

	video, err := db.CreateVideo(ctx, userID, "https://www.youtube.com/watch?v=jNQXAC9IVRw")
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}
	t.Cleanup(func() { db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, video.ID) })

	if err := db.UpdateVideoStatus(ctx, video.ID, models.VideoStatusReady); err != nil {
		t.Fatalf("UpdateVideoStatus: %v", err)
	}

	var status string
	err = db.QueryRow(ctx, `SELECT status FROM videos WHERE id = $1`, video.ID).Scan(&status)
	if err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != string(models.VideoStatusReady) {
		t.Errorf("status = %q, want %q", status, models.VideoStatusReady)
	}
}

func TestInsertChunks_Live(t *testing.T) {
	db, userID := testDB(t)
	ctx := context.Background()

	video, err := db.CreateVideo(ctx, userID, "https://www.youtube.com/watch?v=jNQXAC9IVRw")
	if err != nil {
		t.Fatalf("CreateVideo: %v", err)
	}
	t.Cleanup(func() { db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, video.ID) })

	chunks := []models.Chunk{
		{VideoID: video.ID, ChunkIndex: 0, ChunkText: "first chunk", StartSeconds: 0, EndSeconds: 5, Embedding: make([]float32, 1536)},
		{VideoID: video.ID, ChunkIndex: 1, ChunkText: "second chunk", StartSeconds: 5, EndSeconds: 10, Embedding: make([]float32, 1536)},
	}

	if err := db.InsertChunks(ctx, chunks); err != nil {
		t.Fatalf("InsertChunks: %v", err)
	}

	var count int
	err = db.QueryRow(ctx, `SELECT COUNT(*) FROM chunks WHERE video_id = $1`, video.ID).Scan(&count)
	if err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if count != 2 {
		t.Errorf("chunk count = %d, want 2", count)
	}

	// UNIQUE(video_id, chunk_index) should reject a duplicate index.
	dup := []models.Chunk{
		{VideoID: video.ID, ChunkIndex: 0, ChunkText: "duplicate", StartSeconds: 0, EndSeconds: 1, Embedding: make([]float32, 1536)},
	}
	if err := db.InsertChunks(ctx, dup); err == nil {
		t.Error("expected an error inserting a duplicate chunk_index, got nil")
	}

	// ON DELETE CASCADE should remove chunks when the video is deleted.
	if _, err := db.Exec(ctx, `DELETE FROM videos WHERE id = $1`, video.ID); err != nil {
		t.Fatalf("delete video: %v", err)
	}
	err = db.QueryRow(ctx, `SELECT COUNT(*) FROM chunks WHERE video_id = $1`, video.ID).Scan(&count)
	if err != nil {
		t.Fatalf("count chunks after cascade: %v", err)
	}
	if count != 0 {
		t.Errorf("expected chunks to cascade-delete, got %d remaining", count)
	}
}
