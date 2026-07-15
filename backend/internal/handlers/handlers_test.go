package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/models"
	"verbatim/backend/internal/services"
)

// liveTestVideo is the same fixture URL used by the other live tests in this codebase.
const liveTestVideo = "https://www.youtube.com/watch?v=jNQXAC9IVRw"

// newTestServer wires a VideoHandler behind the same routes main.go registers.
func newTestServer(t *testing.T, db *database.DB, supadataKey, openaiKey string) *httptest.Server {
	t.Helper()
	h := NewVideoHandler(db, services.NewClient(supadataKey), services.NewEmbeddingClient(openaiKey))
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/videos", h.HandleSubmit)
	mux.HandleFunc("GET /api/videos/{id}", h.HandleStatus)
	return httptest.NewServer(mux)
}

// TestVideoHandlers_Live drives HandleSubmit and HandleStatus over real HTTP
// against the real Supadata/OpenAI APIs and a real local Postgres, matching
// this codebase's existing live-test gating pattern.
func TestVideoHandlers_Live(t *testing.T) {
	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	supadataKey := os.Getenv("SUPADATA_API_KEY")
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if databaseURL == "" || supadataKey == "" || openaiKey == "" {
		t.Skip("DATABASE_URL, SUPADATA_API_KEY, and OPENAI_API_KEY must all be set; skipping live handler test")
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
		"handlers-test@example.com", "test-hash",
	).Scan(&userID)
	if err != nil {
		t.Fatalf("upsert test user: %v", err)
	}

	srv := newTestServer(t, db, supadataKey, openaiKey)
	defer srv.Close()

	// Submit: should return 202 immediately with status=pending.
	body, _ := json.Marshal(map[string]string{"video_url": liveTestVideo, "lang": "en", "user_id": userID})
	resp, err := http.Post(srv.URL+"/api/videos", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/videos: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	var video models.Video
	if err := json.NewDecoder(resp.Body).Decode(&video); err != nil {
		t.Fatalf("decode submit response: %v", err)
	}
	defer db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, video.ID)

	if video.Status != models.VideoStatusPending {
		t.Errorf("submit status field = %q, want %q", video.Status, models.VideoStatusPending)
	}

	// Poll until the background goroutine finishes (ready or failed), or time out.
	deadline := time.Now().Add(30 * time.Second)
	var finalStatus models.VideoStatus
	for time.Now().Before(deadline) {
		statusResp, err := http.Get(srv.URL + "/api/videos/" + video.ID)
		if err != nil {
			t.Fatalf("GET /api/videos/{id}: %v", err)
		}
		var polled models.Video
		decodeErr := json.NewDecoder(statusResp.Body).Decode(&polled)
		statusResp.Body.Close()
		if decodeErr != nil {
			t.Fatalf("decode status response: %v", decodeErr)
		}
		if statusResp.StatusCode != http.StatusOK {
			t.Fatalf("poll status = %d, want %d", statusResp.StatusCode, http.StatusOK)
		}
		if polled.Status == models.VideoStatusReady || polled.Status == models.VideoStatusFailed {
			finalStatus = polled.Status
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if finalStatus != models.VideoStatusReady {
		t.Fatalf("final status = %q, want %q", finalStatus, models.VideoStatusReady)
	}

	var chunkCount int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM chunks WHERE video_id = $1`, video.ID).Scan(&chunkCount); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if chunkCount == 0 {
		t.Error("expected at least one chunk to be stored")
	}

	// 404 for a well-formed but nonexistent video ID.
	notFoundResp, err := http.Get(srv.URL + "/api/videos/00000000-0000-0000-0000-000000000000")
	if err != nil {
		t.Fatalf("GET nonexistent video: %v", err)
	}
	notFoundResp.Body.Close()
	if notFoundResp.StatusCode != http.StatusNotFound {
		t.Errorf("nonexistent video status = %d, want %d", notFoundResp.StatusCode, http.StatusNotFound)
	}

	// 400 for a malformed video ID.
	badIDResp, err := http.Get(srv.URL + "/api/videos/not-a-uuid")
	if err != nil {
		t.Fatalf("GET malformed video id: %v", err)
	}
	badIDResp.Body.Close()
	if badIDResp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed id status = %d, want %d", badIDResp.StatusCode, http.StatusBadRequest)
	}

	// 400 for a missing field.
	badBody, _ := json.Marshal(map[string]string{"video_url": liveTestVideo})
	badResp, err := http.Post(srv.URL+"/api/videos", "application/json", bytes.NewReader(badBody))
	if err != nil {
		t.Fatalf("POST missing fields: %v", err)
	}
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing-fields status = %d, want %d", badResp.StatusCode, http.StatusBadRequest)
	}

	// 400 for a well-formed but nonexistent user_id.
	badUserBody, _ := json.Marshal(map[string]string{"video_url": liveTestVideo, "lang": "en", "user_id": "00000000-0000-0000-0000-000000000000"})
	badUserResp, err := http.Post(srv.URL+"/api/videos", "application/json", bytes.NewReader(badUserBody))
	if err != nil {
		t.Fatalf("POST nonexistent user_id: %v", err)
	}
	badUserResp.Body.Close()
	if badUserResp.StatusCode != http.StatusBadRequest {
		t.Errorf("nonexistent user_id status = %d, want %d", badUserResp.StatusCode, http.StatusBadRequest)
	}
}
