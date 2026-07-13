package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"

	"verbatim/backend/internal/database"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, reading DATABASE_URL from environment")
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	ctx := context.Background()

	db, err := database.Connect(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	fmt.Println("Connected to database.")

	// Insert a test user (no-op if it already exists)
	_, err = db.Exec(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2) ON CONFLICT (email) DO NOTHING`,
		"test@example.com", "test-hash",
	)
	if err != nil {
		log.Fatal(err)
	}

	// Query all users
	rows, err := db.Query(ctx, "SELECT id, email FROM users")
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	fmt.Println("Users in database:")
	for rows.Next() {
		var id, email string
		if err := rows.Scan(&id, &email); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %s  %s\n", id, email)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
}
