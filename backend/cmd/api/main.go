package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/handlers"
	"verbatim/backend/internal/middleware"
	"verbatim/backend/internal/services"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, reading config from environment")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	supadataKey := os.Getenv("SUPADATA_API_KEY")
	if supadataKey == "" {
		log.Fatal("SUPADATA_API_KEY is not set")
	}
	openaiKey := os.Getenv("OPENAI_API_KEY")
	if openaiKey == "" {
		log.Fatal("OPENAI_API_KEY is not set")
	}
	s3Bucket := os.Getenv("AWS_S3_BUCKET")
	if s3Bucket == "" {
		log.Fatal("AWS_S3_BUCKET is not set")
	}
	frontendOrigin := os.Getenv("FRONTEND_ORIGIN")
	if frontendOrigin == "" {
		frontendOrigin = "http://localhost:5173"
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET is not set")
	}

	ctx := context.Background()

	db, err := database.Connect(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	log.Println("Connected to database.")

	transcriptClient := services.NewClient(supadataKey)
	openAIClient := services.NewOpenAIClient(openaiKey)
	s3Client, err := services.NewS3Client(ctx, s3Bucket)
	if err != nil {
		log.Fatal(err)
	}
	authService := services.NewAuthService(jwtSecret)
	videoHandler := handlers.NewVideoHandler(db, transcriptClient, openAIClient, s3Client)

	mux := http.NewServeMux()
	authMW := middleware.AuthMiddleware(authService)
	mux.Handle("POST /api/videos", authMW(http.HandlerFunc(videoHandler.HandleSubmit)))
	mux.Handle("GET /api/videos/{id}", authMW(http.HandlerFunc(videoHandler.HandleStatus)))
	mux.Handle("POST /api/videos/{id}/ask", authMW(http.HandlerFunc(videoHandler.HandleAsk)))
	mux.Handle("POST /api/videos/upload", authMW(http.HandlerFunc(videoHandler.HandleUpload)))
	mux.HandleFunc("POST /api/auth/register", handlers.HandleRegister(db, authService))
	mux.HandleFunc("POST /api/auth/login", handlers.HandleLogin(db, authService))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: handlers.CorsMiddleware(frontendOrigin, mux),
		// This server sits directly on the internet in production (behind
		// Nginx), so bound how long a slow/stalled client can hold a
		// connection open instead of relying on Go's unlimited defaults.
		// ReadHeaderTimeout is header-only, so 10s is plenty. ReadTimeout/
		// WriteTimeout cover the whole request including the handler body,
		// and HandleUpload does a *synchronous* S3 upload of up to
		// maxUploadSize (1 GiB, handlers.go) before backgrounding the rest
		// of the pipeline — so these match the same 10-minute bound already
		// used for the background ingestion jobs (handlers.go's
		// processTimeout), not an arbitrary short value that would cut off
		// a legitimate large upload or a slow GPT-4o answer.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Minute,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}

	// Run the server in a goroutine so the main goroutine is free to wait for
	// a shutdown signal below.
	go func() {
		log.Printf("Listening on :%s", port)
		log.Println("  POST /api/auth/register    - create a new user account")
		log.Println("  POST /api/auth/login       - log in and receive a token")
		log.Println("  POST /api/videos           - submit a YouTube video for ingestion (auth required)")
		log.Println("  POST /api/videos/upload    - upload a video/audio file for ingestion (auth required)")
		log.Println("  GET  /api/videos/{id}      - poll a video's ingestion status (auth required)")
		log.Println("  POST /api/videos/{id}/ask  - ask a question about a video (auth required)")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	// Block until Docker (or an operator) sends SIGTERM/SIGINT, e.g. on
	// `docker stop` or a redeploy, then drain in-flight requests instead of
	// dropping them.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	log.Println("Shutting down: draining in-flight requests...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown did not complete cleanly: %v", err)
	}
	// db.Close() (deferred above) now runs, closing the pool only after
	// every in-flight request has finished or the timeout elapsed.
}
