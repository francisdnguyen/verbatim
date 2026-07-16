package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/handlers"
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

	ctx := context.Background()

	db, err := database.Connect(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	log.Println("Connected to database.")

	transcriptClient := services.NewClient(supadataKey)
	embedClient := services.NewEmbeddingClient(openaiKey)
	videoHandler := handlers.NewVideoHandler(db, transcriptClient, embedClient)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/videos", videoHandler.HandleSubmit)
	mux.HandleFunc("GET /api/videos/{id}", videoHandler.HandleStatus)
	mux.HandleFunc("POST /api/videos/{id}/ask", videoHandler.HandleAsk)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Listening on :%s", port)
	log.Println("  POST /api/videos           - submit a YouTube video for ingestion")
	log.Println("  GET  /api/videos/{id}      - poll a video's ingestion status")
	log.Println("  POST /api/videos/{id}/ask  - ask a question about a video")
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
