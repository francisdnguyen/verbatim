package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/middleware"
	"verbatim/backend/internal/models"
	"verbatim/backend/internal/services"
)

// processTimeout bounds one background ingestion job, so a hung external
// call (Supadata/OpenAI) can't leave a goroutine running forever.
const processTimeout = 10 * time.Minute

// maxUploadSize caps a single direct file upload, rejected beyond this with
// 413 rather than accepting an unbounded request body.
const maxUploadSize = 1 << 30 // 1 GiB

// maxJSONBodySize caps every plain-JSON request body — none of these
// payloads (a URL, a question, an email/password pair) legitimately need
// more than a few hundred bytes; this just stops an unbounded body from
// being buffered in memory before json.Decode ever gets to reject it.
const maxJSONBodySize = 1 << 20 // 1 MiB

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

// submitRequest is the JSON body for POST /api/videos. The owning user comes
// from the authenticated caller (see middleware.AuthMiddleware), not the body.
type submitRequest struct {
	VideoURL string `json:"video_url"`
	Lang     string `json:"lang"`
}

// HandleSubmit creates a pending video row and returns immediately, then
// runs the transcript → chunk → embed → store pipeline in the background —
// the frontend polls HandleStatus until the row reaches ready/failed.
func (h *VideoHandler) HandleSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodySize)
	var req submitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.VideoURL == "" || req.Lang == "" {
		writeError(w, http.StatusBadRequest, "video_url and lang are required")
		return
	}
	userID, _ := middleware.GetUserID(r)

	video, err := h.db.CreateVideo(r.Context(), userID, req.VideoURL)
	if errors.Is(err, database.ErrUserNotFound) {
		// Practically unreachable through this route now — a valid token
		// always has a real backing user row — but left as defensive
		// coverage for a token whose user was deleted after issuance.
		writeError(w, http.StatusUnauthorized, "authenticated user no longer exists")
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

	lang := r.FormValue("lang")
	userID, _ := middleware.GetUserID(r)

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
		// Practically unreachable now that userID comes from a validated
		// token rather than a client-supplied field — left as defensive
		// coverage for a token whose user was deleted after issuance. The
		// file already made it to S3 before this was known, so clean it up
		// rather than leaving an orphaned object with no video row to ever
		// reference it. Best-effort: log, don't fail the response over a
		// cleanup failure.
		if delErr := h.s3Client.DeleteObject(context.Background(), s3Key); delErr != nil {
			log.Printf("upload video: cleanup orphaned s3 object %s: %v", s3Key, delErr)
		}
		writeError(w, http.StatusUnauthorized, "authenticated user no longer exists")
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
	if callerID, _ := middleware.GetUserID(r); video.UserID != callerID {
		// 404, not 403: confirming a non-owner's guessed UUID is real would
		// leak the video's existence. Treating "not yours" the same as
		// "doesn't exist" keeps video IDs unenumerable by non-owners.
		writeError(w, http.StatusNotFound, "video not found")
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

	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodySize)
	var req askRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Question == "" {
		writeError(w, http.StatusBadRequest, "question is required")
		return
	}

	// Look up the video first, purely to gate on ownership before spending
	// an OpenAI call on a request that's about to be rejected —
	// AnswerQuestion does its own internal lookup too, a small deliberate
	// extra round-trip rather than threading the caller ID through it.
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
		log.Printf("answer question for video %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "failed to answer question")
		return
	}
	if callerID, _ := middleware.GetUserID(r); video.UserID != callerID {
		writeError(w, http.StatusNotFound, "video not found")
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

// registerRequest is the JSON body for POST /api/auth/register.
type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginRequest is the JSON body for POST /api/auth/login.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// userResponse is the JSON body returned by HandleRegister/HandleLogin/
// HandleMe. The JWT itself is never included here — it only ever travels as
// an httpOnly cookie, never somewhere client-side JS could read it.
// models.User.PasswordHash already carries json:"-", so it's never
// serialized here either.
type userResponse struct {
	User models.User `json:"user"`
}

// minPasswordLength is the minimum acceptable length for a new password —
// a basic sanity floor, not a full password-strength policy.
const minPasswordLength = 8

// normalizeEmail trims whitespace and lowercases an email so that
// "Foo@x.com" and " foo@x.com " register/look up as the same account —
// neither Postgres's UNIQUE constraint nor a plain WHERE lookup does this
// for you.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// generateCSRFToken returns a random hex string for the double-submit CSRF
// cookie — nothing here needs to be verifiable/signed like the JWT, just
// unguessable, since its only job is proving the request-issuing JS could
// read the cookie (i.e. is running on the real frontend origin).
func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate csrf token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// setAuthCookies sets the httpOnly session cookie and the JS-readable CSRF
// cookie together, sized to expire alongside the JWT itself (services.TokenTTL).
// secure/sameSite follow the secureCookies flag main.go derives from
// COOKIE_SECURE: production (cross-site, HTTPS both ends) needs
// SameSite=None+Secure for the browser to send the cookie at all; local dev
// (plain HTTP) can't set Secure, and SameSite=None without Secure is
// rejected by browsers outright, so it falls back to SameSite=Lax instead.
func setAuthCookies(w http.ResponseWriter, token, csrfToken string, secureCookies bool) {
	sameSite := http.SameSiteLaxMode
	if secureCookies {
		sameSite = http.SameSiteNoneMode
	}
	maxAge := int(services.TokenTTL.Seconds())
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.TokenCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secureCookies,
		SameSite: sameSite,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.CSRFCookieName,
		Value:    csrfToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: false, // the frontend must read this to echo it in X-CSRF-Token
		Secure:   secureCookies,
		SameSite: sameSite,
	})
}

// clearAuthCookies expires both auth cookies immediately, for logout.
func clearAuthCookies(w http.ResponseWriter, secureCookies bool) {
	sameSite := http.SameSiteLaxMode
	if secureCookies {
		sameSite = http.SameSiteNoneMode
	}
	for _, name := range []string{middleware.TokenCookieName, middleware.CSRFCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == middleware.TokenCookieName,
			Secure:   secureCookies,
			SameSite: sameSite,
		})
	}
}

// HandleRegister creates a new user and starts a session for it — a
// standalone function (not a VideoHandler method) since it only needs the
// DB and auth service, not the video-specific clients.
func HandleRegister(db *database.DB, authService *services.AuthService, secureCookies bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodySize)
		var req registerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Email == "" || req.Password == "" {
			writeError(w, http.StatusBadRequest, "email and password are required")
			return
		}
		if len(req.Password) < minPasswordLength {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("password must be at least %d characters", minPasswordLength))
			return
		}
		email := normalizeEmail(req.Email)

		passwordHash, err := authService.HashPassword(req.Password)
		if err != nil {
			log.Printf("register: hash password: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to register")
			return
		}

		user, err := db.CreateUser(r.Context(), email, passwordHash)
		if errors.Is(err, database.ErrEmailTaken) {
			writeError(w, http.StatusConflict, "email already registered")
			return
		}
		if err != nil {
			log.Printf("register: create user: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to register")
			return
		}

		token, err := authService.GenerateToken(user.ID, user.Email)
		if err != nil {
			log.Printf("register: generate token: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to register")
			return
		}
		csrfToken, err := generateCSRFToken()
		if err != nil {
			log.Printf("register: generate csrf token: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to register")
			return
		}

		setAuthCookies(w, token, csrfToken, secureCookies)
		writeJSON(w, http.StatusCreated, userResponse{User: user})
	}
}

// HandleLogin authenticates a user and starts a session for it — a
// standalone function for the same reason as HandleRegister.
func HandleLogin(db *database.DB, authService *services.AuthService, secureCookies bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodySize)
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Email == "" || req.Password == "" {
			writeError(w, http.StatusBadRequest, "email and password are required")
			return
		}

		user, err := db.GetUserByEmail(r.Context(), normalizeEmail(req.Email))
		if errors.Is(err, database.ErrUserNotFound) {
			// Deliberately the same response as a wrong password below —
			// distinguishing "no such user" from "wrong password" would let
			// a caller enumerate registered emails.
			writeError(w, http.StatusUnauthorized, "invalid email or password")
			return
		}
		if err != nil {
			log.Printf("login: get user by email: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to log in")
			return
		}
		if !authService.CheckPassword(user.PasswordHash, req.Password) {
			writeError(w, http.StatusUnauthorized, "invalid email or password")
			return
		}

		token, err := authService.GenerateToken(user.ID, user.Email)
		if err != nil {
			log.Printf("login: generate token: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to log in")
			return
		}
		csrfToken, err := generateCSRFToken()
		if err != nil {
			log.Printf("login: generate csrf token: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to log in")
			return
		}

		setAuthCookies(w, token, csrfToken, secureCookies)
		writeJSON(w, http.StatusOK, userResponse{User: user})
	}
}

// HandleLogout clears the session/CSRF cookies. Not CSRF-protected itself —
// worst case a forged cross-site logout just deauthenticates the caller,
// which isn't the kind of state change CSRF protection exists to prevent.
func HandleLogout(secureCookies bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clearAuthCookies(w, secureCookies)
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleMe returns the currently authenticated caller's user record — the
// session-bootstrap endpoint the frontend calls on load to find out who (if
// anyone) is logged in, now that the token itself lives in an httpOnly
// cookie the frontend's JS can't read directly.
func HandleMe(db *database.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _ := middleware.GetUserID(r)
		user, err := db.GetUserByID(r.Context(), userID)
		if errors.Is(err, database.ErrUserNotFound) {
			// A validly-signed session cookie for a user deleted after the
			// token was issued — same 401 as "no session at all" so the
			// frontend's getCurrentUser() treats it as logged-out instead of
			// throwing, rather than a 500 it doesn't know how to handle.
			writeError(w, http.StatusUnauthorized, "session no longer valid")
			return
		}
		if err != nil {
			log.Printf("me: get user %s: %v", userID, err)
			writeError(w, http.StatusInternalServerError, "failed to load user")
			return
		}
		writeJSON(w, http.StatusOK, userResponse{User: user})
	}
}

// CorsMiddleware allows cross-origin requests from allowedOrigin (the
// frontend dev server or, in production, the deployed Vercel origin) and
// answers the browser's preflight OPTIONS request directly, since the mux
// has no route registered for it otherwise. Allow-Credentials is required
// for the browser to actually send/receive the auth cookies cross-origin;
// browsers refuse Allow-Credentials paired with a wildcard origin, which is
// exactly why allowedOrigin must stay a specific value, never "*".
func CorsMiddleware(allowedOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-CSRF-Token")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
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
