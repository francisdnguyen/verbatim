package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
// s3Client may be a client for a bucket that doesn't actually get used
// (harmless for tests that never hit HandleUpload) — services.NewS3Client
// doesn't validate credentials/bucket existence eagerly.
func newTestServer(t *testing.T, db *database.DB, supadataKey, openaiKey string, s3Client *services.S3Client) *httptest.Server {
	t.Helper()
	h := NewVideoHandler(db, services.NewClient(supadataKey), services.NewOpenAIClient(openaiKey), s3Client)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/videos", h.HandleSubmit)
	mux.HandleFunc("GET /api/videos/{id}", h.HandleStatus)
	mux.HandleFunc("POST /api/videos/{id}/ask", h.HandleAsk)
	mux.HandleFunc("POST /api/videos/upload", h.HandleUpload)
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

	s3Client, err := services.NewS3Client(ctx, os.Getenv("AWS_S3_BUCKET"))
	if err != nil {
		t.Fatalf("new s3 client: %v", err)
	}

	srv := newTestServer(t, db, supadataKey, openaiKey, s3Client)
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

	// Ask a real question about the now-ready video: 200 with a real answer and sources.
	askBody, _ := json.Marshal(map[string]string{"question": "What is this video about?"})
	askResp, err := http.Post(srv.URL+"/api/videos/"+video.ID+"/ask", "application/json", bytes.NewReader(askBody))
	if err != nil {
		t.Fatalf("POST ask: %v", err)
	}
	defer askResp.Body.Close()
	if askResp.StatusCode != http.StatusOK {
		t.Fatalf("ask status = %d, want %d", askResp.StatusCode, http.StatusOK)
	}
	var ask askResponse
	if err := json.NewDecoder(askResp.Body).Decode(&ask); err != nil {
		t.Fatalf("decode ask response: %v", err)
	}
	if ask.Answer == "" {
		t.Error("expected a non-empty answer")
	}
	if len(ask.Sources) == 0 {
		t.Error("expected at least one source")
	}

	// 404 asking about a nonexistent video.
	askNotFoundResp, err := http.Post(srv.URL+"/api/videos/00000000-0000-0000-0000-000000000000/ask", "application/json", bytes.NewReader(askBody))
	if err != nil {
		t.Fatalf("POST ask nonexistent video: %v", err)
	}
	askNotFoundResp.Body.Close()
	if askNotFoundResp.StatusCode != http.StatusNotFound {
		t.Errorf("ask nonexistent video status = %d, want %d", askNotFoundResp.StatusCode, http.StatusNotFound)
	}

	// 400 asking with an empty question.
	emptyQuestionBody, _ := json.Marshal(map[string]string{"question": ""})
	askEmptyResp, err := http.Post(srv.URL+"/api/videos/"+video.ID+"/ask", "application/json", bytes.NewReader(emptyQuestionBody))
	if err != nil {
		t.Fatalf("POST ask empty question: %v", err)
	}
	askEmptyResp.Body.Close()
	if askEmptyResp.StatusCode != http.StatusBadRequest {
		t.Errorf("ask empty question status = %d, want %d", askEmptyResp.StatusCode, http.StatusBadRequest)
	}

	// 409 asking about a video that hasn't finished processing yet. Relies on
	// the background ingestion goroutine not having finished by the time the
	// ask call fires a few milliseconds after submit returns — true for this
	// short fixture video today (confirmed: ingestion takes ~2s, this call
	// fires within milliseconds of the 202), but a timing assumption, not a
	// guarantee. Revisit if this ever flakes.
	submitBody, _ := json.Marshal(map[string]string{"video_url": liveTestVideo, "lang": "en", "user_id": userID})
	submitResp, err := http.Post(srv.URL+"/api/videos", "application/json", bytes.NewReader(submitBody))
	if err != nil {
		t.Fatalf("POST /api/videos (for not-ready check): %v", err)
	}
	var pendingVideo models.Video
	decodeErr := json.NewDecoder(submitResp.Body).Decode(&pendingVideo)
	submitResp.Body.Close()
	if decodeErr != nil {
		t.Fatalf("decode second submit response: %v", decodeErr)
	}
	defer db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, pendingVideo.ID)

	askNotReadyResp, err := http.Post(srv.URL+"/api/videos/"+pendingVideo.ID+"/ask", "application/json", bytes.NewReader(askBody))
	if err != nil {
		t.Fatalf("POST ask not-ready video: %v", err)
	}
	askNotReadyResp.Body.Close()
	if askNotReadyResp.StatusCode != http.StatusConflict {
		t.Errorf("ask not-ready video status = %d, want %d", askNotReadyResp.StatusCode, http.StatusConflict)
	}
}

// TestVideoHandlers_Live_Upload drives HandleUpload over real HTTP against
// a real S3 bucket, ffmpeg, and Whisper — skips unless AWS_S3_BUCKET (plus
// AWS credentials) is set and the ffmpeg binary is on PATH, matching this
// codebase's skip-without-external-dependency convention.
func TestVideoHandlers_Live_Upload(t *testing.T) {
	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	supadataKey := os.Getenv("SUPADATA_API_KEY")
	openaiKey := os.Getenv("OPENAI_API_KEY")
	s3Bucket := os.Getenv("AWS_S3_BUCKET")
	if databaseURL == "" || supadataKey == "" || openaiKey == "" || s3Bucket == "" {
		t.Skip("DATABASE_URL, SUPADATA_API_KEY, OPENAI_API_KEY, and AWS_S3_BUCKET must all be set; skipping live upload test")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH; skipping live upload test")
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
		"handlers-upload-test@example.com", "test-hash",
	).Scan(&userID)
	if err != nil {
		t.Fatalf("upsert test user: %v", err)
	}

	s3Client, err := services.NewS3Client(ctx, s3Bucket)
	if err != nil {
		t.Fatalf("new s3 client: %v", err)
	}

	srv := newTestServer(t, db, supadataKey, openaiKey, s3Client)
	defer srv.Close()

	fixturePath := filepath.Join("testdata", "sample-speech.wav")
	fixture, err := os.Open(fixturePath)
	if err != nil {
		t.Fatalf("open fixture %s: %v", fixturePath, err)
	}
	defer fixture.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("user_id", userID); err != nil {
		t.Fatalf("write user_id field: %v", err)
	}
	if err := writer.WriteField("lang", "en"); err != nil {
		t.Fatalf("write lang field: %v", err)
	}
	part, err := writer.CreateFormFile("file", "sample-speech.wav")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, fixture); err != nil {
		t.Fatalf("copy fixture into form: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	resp, err := http.Post(srv.URL+"/api/videos/upload", writer.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("POST /api/videos/upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	var video models.Video
	if err := json.NewDecoder(resp.Body).Decode(&video); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	defer db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, video.ID)

	if video.Status != models.VideoStatusPending {
		t.Errorf("upload status field = %q, want %q", video.Status, models.VideoStatusPending)
	}
	if video.S3Key == nil || *video.S3Key == "" {
		t.Error("expected a non-empty s3_key on the created video")
	}

	// Poll until the background goroutine finishes (ready or failed), or time out.
	deadline := time.Now().Add(60 * time.Second)
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

	// Confirm Whisper actually transcribed the fixture's real speech content
	// ("...The elephant has a very long trunk.") rather than storing empty
	// or garbage text — catches a regression in the response-mapping code
	// that a bare chunkCount>0 check wouldn't.
	var chunkText string
	if err := db.QueryRow(ctx, `SELECT chunk_text FROM chunks WHERE video_id = $1 ORDER BY chunk_index LIMIT 1`, video.ID).Scan(&chunkText); err != nil {
		t.Fatalf("query chunk text: %v", err)
	}
	if !strings.Contains(strings.ToLower(chunkText), "elephant") {
		t.Errorf("chunk text = %q, want it to contain the fixture's actual speech content", chunkText)
	}
}

// TestVideoHandlers_Live_Upload_Failure confirms a non-media upload fails
// ffmpeg's extraction step and correctly lands the video on 'failed'
// instead of hanging at 'processing' forever — the upload path's
// counterpart to the existing YouTube-path failure test.
func TestVideoHandlers_Live_Upload_Failure(t *testing.T) {
	_ = godotenv.Load("../../.env")

	databaseURL := os.Getenv("DATABASE_URL")
	supadataKey := os.Getenv("SUPADATA_API_KEY")
	openaiKey := os.Getenv("OPENAI_API_KEY")
	s3Bucket := os.Getenv("AWS_S3_BUCKET")
	if databaseURL == "" || supadataKey == "" || openaiKey == "" || s3Bucket == "" {
		t.Skip("DATABASE_URL, SUPADATA_API_KEY, OPENAI_API_KEY, and AWS_S3_BUCKET must all be set; skipping live upload test")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found on PATH; skipping live upload test")
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
		"handlers-upload-test@example.com", "test-hash",
	).Scan(&userID)
	if err != nil {
		t.Fatalf("upsert test user: %v", err)
	}

	s3Client, err := services.NewS3Client(ctx, s3Bucket)
	if err != nil {
		t.Fatalf("new s3 client: %v", err)
	}

	srv := newTestServer(t, db, supadataKey, openaiKey, s3Client)
	defer srv.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("user_id", userID); err != nil {
		t.Fatalf("write user_id field: %v", err)
	}
	part, err := writer.CreateFormFile("file", "not-media.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("this is plain text, not a real media file")); err != nil {
		t.Fatalf("write fake file content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	resp, err := http.Post(srv.URL+"/api/videos/upload", writer.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("POST /api/videos/upload: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	var video models.Video
	if err := json.NewDecoder(resp.Body).Decode(&video); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	defer db.Exec(context.Background(), `DELETE FROM videos WHERE id = $1`, video.ID)

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
		if polled.Status == models.VideoStatusReady || polled.Status == models.VideoStatusFailed {
			finalStatus = polled.Status
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if finalStatus != models.VideoStatusFailed {
		t.Fatalf("final status = %q, want %q", finalStatus, models.VideoStatusFailed)
	}
}
