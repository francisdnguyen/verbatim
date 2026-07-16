package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/models"
	"verbatim/backend/internal/services"
)

// processTimeout bounds one background ingestion job, so a hung external
// call (Supadata/OpenAI) can't leave a goroutine running forever.
const processTimeout = 10 * time.Minute

// VideoHandler serves the video ingestion and Q&A HTTP endpoints, wrapping
// the DB and the external service clients the async pipeline and Q&A need.
type VideoHandler struct {
	db               *database.DB
	transcriptClient *services.Client
	embedClient      *services.EmbeddingClient
}

// NewVideoHandler builds a VideoHandler from its dependencies.
func NewVideoHandler(db *database.DB, transcriptClient *services.Client, embedClient *services.EmbeddingClient) *VideoHandler {
	return &VideoHandler{db: db, transcriptClient: transcriptClient, embedClient: embedClient}
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
		if err := services.ProcessVideo(ctx, h.db, h.transcriptClient, h.embedClient, video, req.VideoURL, req.Lang); err != nil {
			log.Printf("process video %s: %v", video.ID, err)
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

	answer, err := services.AnswerQuestion(r.Context(), h.db, h.embedClient, id, req.Question)
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
