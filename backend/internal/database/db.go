package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	"verbatim/backend/internal/models"
)

// ErrVideoNotFound is returned when a video ID doesn't match any row, so
// callers (like the status-poll HTTP handler) can return a 404 instead of a
// generic 500.
var ErrVideoNotFound = errors.New("video not found")

// ErrInvalidID is returned when a caller-supplied ID isn't a validly
// formatted UUID, so callers can return a 400 instead of a generic 500.
var ErrInvalidID = errors.New("invalid id")

// ErrUserNotFound is returned when a video is created for a user_id that
// doesn't exist, so callers can return a 400 instead of a generic 500.
var ErrUserNotFound = errors.New("user not found")

// pgErrorCode returns err's Postgres error code (e.g. "23503"), or "" if err
// isn't a *pgconn.PgError.
func pgErrorCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// DB wraps a pgx connection pool with the app's repository methods. Embedding
// *pgxpool.Pool keeps Exec/Query/Close usable directly, alongside the named
// methods below.
type DB struct {
	*pgxpool.Pool
}

// Connect opens a pgx connection pool and pings it to fail fast on bad config.
// Every pooled connection registers pgvector's type so vector(1536) columns
// can be read/written as pgvector.Vector.
func Connect(ctx context.Context, databaseURL string) (*DB, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &DB{Pool: pool}, nil
}

// CreateVideo inserts a new YouTube-sourced video row (status starts 'pending').
func (db *DB) CreateVideo(ctx context.Context, userID, sourceURL string) (models.Video, error) {
	var v models.Video
	err := db.QueryRow(ctx,
		`INSERT INTO videos (user_id, source_type, source_url)
		 VALUES ($1, 'youtube', $2)
		 RETURNING id, user_id, source_type, source_url, s3_key, title, status, created_at, updated_at`,
		userID, sourceURL,
	).Scan(&v.ID, &v.UserID, &v.SourceType, &v.SourceURL, &v.S3Key, &v.Title, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	if pgErrorCode(err) == "23503" { // foreign_key_violation: user_id doesn't exist
		return models.Video{}, ErrUserNotFound
	}
	if err != nil {
		return models.Video{}, fmt.Errorf("create video: %w", err)
	}
	return v, nil
}

// GetVideo looks up a single video row by ID, returning ErrVideoNotFound if
// no such row exists.
func (db *DB) GetVideo(ctx context.Context, videoID string) (models.Video, error) {
	var v models.Video
	err := db.QueryRow(ctx,
		`SELECT id, user_id, source_type, source_url, s3_key, title, status, created_at, updated_at
		 FROM videos WHERE id = $1`,
		videoID,
	).Scan(&v.ID, &v.UserID, &v.SourceType, &v.SourceURL, &v.S3Key, &v.Title, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Video{}, ErrVideoNotFound
	}
	if pgErrorCode(err) == "22P02" { // invalid_text_representation: videoID isn't a valid UUID
		return models.Video{}, ErrInvalidID
	}
	if err != nil {
		return models.Video{}, fmt.Errorf("get video: %w", err)
	}
	return v, nil
}

// UpdateVideoStatus transitions a video's status (processing/ready/failed).
func (db *DB) UpdateVideoStatus(ctx context.Context, videoID string, status models.VideoStatus) error {
	_, err := db.Exec(ctx, `UPDATE videos SET status = $1 WHERE id = $2`, status, videoID)
	if err != nil {
		return fmt.Errorf("update video status: %w", err)
	}
	return nil
}

// InsertChunks writes all chunks for one video inside a single transaction —
// either the whole set lands or none does, so a partial-write video never exists.
func (db *DB) InsertChunks(ctx context.Context, chunks []models.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("insert chunks: begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) // no-op if the transaction was already committed

	for _, c := range chunks {
		_, err := tx.Exec(ctx,
			`INSERT INTO chunks (video_id, chunk_index, chunk_text, start_seconds, end_seconds, embedding)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			c.VideoID, c.ChunkIndex, c.ChunkText, c.StartSeconds, c.EndSeconds, pgvector.NewVector(c.Embedding),
		)
		if err != nil {
			return fmt.Errorf("insert chunks: chunk %d: %w", c.ChunkIndex, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("insert chunks: commit: %w", err)
	}
	return nil
}
