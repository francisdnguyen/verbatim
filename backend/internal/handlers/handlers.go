package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/models"
	"verbatim/backend/internal/services"
)

// processTimeout bounds one background ingestion job, so a hung external
// call (Supadata/OpenAI) can't leave a goroutine running forever.
const processTimeout = 10 * time.Minute

// maxUploadSize caps a single direct file upload, rejected beyond this with
// 413 rather than accepting an unbounded request body.
const maxUploadSize = 1 << 30 // 1 GiB

// VideoHandler serves the video ingestion and Q&A HTTP endpoints, wrapping
// the DB and the external service clients the async pipeline and Q&A need.
type VideoHandler struct {
	db               *database.DB
	transcriptClient *services.Client
	openAIClient     *services.OpenAIClient
	s3Client         *services.S3Client
}

// NewVideoHandler builds a VideoHandler from its dependencies.
func NewVideoHandler(db *database.DB, transcriptClient *services.Client, openAIClient *services.OpenAIClient, s3Client *services.S3Client) *VideoHandler {
	return &VideoHandler{db: db, transcriptClient: transcriptClient, openAIClient: openAIClient, s3Client: s3Client}
}

// submitRequest is the JSON body for POST /api/videos. user_id is a plain
// request field (not derived from auth) because JWT auth hasn't landed yet
// — Phase 2 replaces this with the authenticated caller's ID.
type submitRequest struct {
	VideoURL string `json:"video_url"`
	Lang     string `json:"lang"`
	UserID   string `json:"user_id"`
}

// HandleSubmit creates a pending video row and returns immediately, then
// runs the transcript → chunk → embed → store pipeline in the background —
// the frontend polls HandleStatus until the row reaches ready/failed.
func (h *VideoHandler) HandleSubmit(w http.ResponseWriter, r *http.Request) {
	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.VideoURL == "" || req.Lang == "" || req.UserID == "" {
		writeError(w, http.StatusBadRequest, "video_url, lang, and user_id are all required")
		return
	}

	video, err := h.db.CreateVideo(r.Context(), req.UserID, req.VideoURL)
	if errors.Is(err, database.ErrUserNotFound) {
		writeError(w, http.StatusBadRequest, "user_id does not exist")
		return
	}
	if err != nil {
		log.Printf("submit video: create video: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to create video")
		return
	}

	// Runs after the response is written, so it must not use the request's
	// context — that gets cancelled the moment HandleSubmit returns. Bounded
	// by processTimeout rather than context.Background() alone, so a hung
	// external call doesn't leak the goroutine forever. A panic here would
	// otherwise crash the whole server (Go only recovers panics in its own
	// per-connection goroutines, not ones spawned by handler code), so it's
	// recovered and turned into a normal failed-video outcome instead.
	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("process video %s: panic: %v", video.ID, p)
				if updateErr := h.db.UpdateVideoStatus(context.Background(), video.ID, models.VideoStatusFailed); updateErr != nil {
					log.Printf("process video %s: also failed to mark video failed after panic: %v", video.ID, updateErr)
				}
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
		defer cancel()
		if err := services.ProcessVideo(ctx, h.db, h.transcriptClient, h.openAIClient, video, req.VideoURL, req.Lang); err != nil {
			log.Printf("process video %s: %v", video.ID, err)
		}
	}()

	writeJSON(w, http.StatusAccepted, video)
}

// HandleUpload accepts a direct video/audio file upload, stores it in S3,
// creates a pending video row, and returns immediately — the same
// respond-now/process-in-background shape as HandleSubmit. Unlike
// HandleSubmit, the S3 upload itself happens synchronously here, before the
// video row exists: the videos table's CHECK constraint requires a real
// s3_key for an 'upload' row, and receiving the multipart body already
// costs as much time as the client's own upload speed regardless of what
// the server does with it afterward, so uploading those same bytes to S3
// before responding doesn't meaningfully worsen "respond quickly." Only the
// slow, externally-bound part (download back, extract audio, transcribe,
// chunk, embed, store) moves to the background goroutine.
func (h *VideoHandler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB in-memory; larger fields spill to temp files automatically
		writeError(w, http.StatusRequestEntityTooLarge, "file too large or invalid form")
		return
	}

	userID := r.FormValue("user_id")
	lang := r.FormValue("lang")
	if userID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	s3Key := fmt.Sprintf("uploads/%s/%d-%s", userID, time.Now().UnixNano(), filepath.Base(header.Filename))
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	if err := h.s3Client.UploadObject(r.Context(), s3Key, file, contentType); err != nil {
		log.Printf("upload video: upload to s3: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to upload file")
		return
	}

	video, err := h.db.CreateUploadedVideo(r.Context(), userID, s3Key, header.Filename)
	if errors.Is(err, database.ErrUserNotFound) {
		// The file already made it to S3 before we knew user_id was bad —
		// clean it up rather than leaving an orphaned object with no video
		// row to ever reference it. Best-effort: log, don't fail the
		// response over a cleanup failure.
		if delErr := h.s3Client.DeleteObject(context.Background(), s3Key); delErr != nil {
			log.Printf("upload video: cleanup orphaned s3 object %s: %v", s3Key, delErr)
		}
		writeError(w, http.StatusBadRequest, "user_id does not exist")
		return
	}
	if err != nil {
		if delErr := h.s3Client.DeleteObject(context.Background(), s3Key); delErr != nil {
			log.Printf("upload video: cleanup orphaned s3 object %s: %v", s3Key, delErr)
		}
		log.Printf("upload video: create video: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to create video")
		return
	}

	// Same async pattern as HandleSubmit's goroutine: detached context with
	// a timeout, panic-recovered so a failure here can't crash the server.
	go func() {
		defer func() {
			if p := recover(); p != nil {
				log.Printf("process uploaded video %s: panic: %v", video.ID, p)
				if updateErr := h.db.UpdateVideoStatus(context.Background(), video.ID, models.VideoStatusFailed); updateErr != nil {
					log.Printf("process uploaded video %s: also failed to mark video failed after panic: %v", video.ID, updateErr)
				}
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), processTimeout)
		defer cancel()
		if err := services.ProcessUploadedVideo(ctx, h.db, h.s3Client, h.openAIClient, video, s3Key, lang); err != nil {
			log.Printf("process uploaded video %s: %v", video.ID, err)
		}
	}()

	writeJSON(w, http.StatusAccepted, video)
}

// HandleStatus returns the current status/row for one video, for the
// frontend to poll after HandleSubmit.
func (h *VideoHandler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	video, err := h.db.GetVideo(r.Context(), id)
	if errors.Is(err, database.ErrVideoNotFound) {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	if errors.Is(err, database.ErrInvalidID) {
		writeError(w, http.StatusBadRequest, "invalid video id")
		return
	}
	if err != nil {
		log.Printf("get video status: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to look up video")
		return
	}

	writeJSON(w, http.StatusOK, video)
}

// askRequest is the JSON body for POST /api/videos/{id}/ask.
type askRequest struct {
	Question string `json:"question"`
}

// askResponse is the JSON body returned by HandleAsk.
type askResponse struct {
	Answer  string            `json:"answer"`
	Sources []services.Source `json:"sources"`
}

// HandleAsk answers a question about one video's content, grounded in its
// stored transcript chunks, with timestamp citations.
func (h *VideoHandler) HandleAsk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Question == "" {
		writeError(w, http.StatusBadRequest, "question is required")
		return
	}

	answer, err := services.AnswerQuestion(r.Context(), h.db, h.openAIClient, id, req.Question)
	if errors.Is(err, database.ErrVideoNotFound) {
		writeError(w, http.StatusNotFound, "video not found")
		return
	}
	if errors.Is(err, database.ErrInvalidID) {
		writeError(w, http.StatusBadRequest, "invalid video id")
		return
	}
	if errors.Is(err, services.ErrVideoNotReady) {
		writeError(w, http.StatusConflict, "video is not ready for questions")
		return
	}
	if err != nil {
		log.Printf("answer question for video %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to answer question")
		return
	}

	writeJSON(w, http.StatusOK, askResponse{Answer: answer.Text, Sources: answer.Sources})
}

// writeJSON encodes v as the JSON response body with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}

// writeError writes a JSON {"error": message} body with the given status code.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
