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

// ErrUserNotFound is returned when a referenced or looked-up user doesn't
// exist — a video created for a nonexistent user_id, or no row matching a
// login attempt's email — so callers can return a 4xx instead of a generic 500.
var ErrUserNotFound = errors.New("user not found")

// ErrEmailTaken is returned when CreateUser is called with an email that
// already has a row, so callers can return a 409 instead of a generic 500.
var ErrEmailTaken = errors.New("email already registered")

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

// CreateUser inserts a new user row with an already-hashed password. Unlike
// the demo-user placeholder this replaces, it's a real insert, not an
// upsert — registration must reject a duplicate email, not silently reuse
// the existing row.
func (db *DB) CreateUser(ctx context.Context, email, passwordHash string) (models.User, error) {
	var u models.User
	err := db.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2)
		 RETURNING id, email, created_at`,
		email, passwordHash,
	).Scan(&u.ID, &u.Email, &u.CreatedAt)
	if pgErrorCode(err) == "23505" { // unique_violation: email already has a row
		return models.User{}, ErrEmailTaken
	}
	if err != nil {
		return models.User{}, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// GetUserByEmail looks up a user by email, returning ErrUserNotFound if no
// such row exists. The only caller (login) needs password_hash, so this is
// the one place outside CreateUser that column is ever read.
func (db *DB) GetUserByEmail(ctx context.Context, email string) (models.User, error) {
	var u models.User
	err := db.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at FROM users WHERE email = $1`,
		email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, ErrUserNotFound
	}
	if err != nil {
		return models.User{}, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

// GetUserByID looks up a user by ID, returning ErrUserNotFound if no such
// row exists — used by the /api/auth/me session-bootstrap endpoint, whose
// only input is the ID already verified inside the caller's JWT.
func (db *DB) GetUserByID(ctx context.Context, userID string) (models.User, error) {
	var u models.User
	err := db.QueryRow(ctx,
		`SELECT id, email, created_at FROM users WHERE id = $1`,
		userID,
	).Scan(&u.ID, &u.Email, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, ErrUserNotFound
	}
	if pgErrorCode(err) == "22P02" { // invalid_text_representation: userID isn't a valid UUID
		return models.User{}, ErrInvalidID
	}
	if err != nil {
		return models.User{}, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
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

// CreateUploadedVideo inserts a new upload-sourced video row (status starts
// 'pending'). title is optional; an empty string is stored as NULL.
func (db *DB) CreateUploadedVideo(ctx context.Context, userID, s3Key, title string) (models.Video, error) {
	var titleArg *string
	if title != "" {
		titleArg = &title
	}

	var v models.Video
	err := db.QueryRow(ctx,
		`INSERT INTO videos (user_id, source_type, s3_key, title)
		 VALUES ($1, 'upload', $2, $3)
		 RETURNING id, user_id, source_type, source_url, s3_key, title, status, created_at, updated_at`,
		userID, s3Key, titleArg,
	).Scan(&v.ID, &v.UserID, &v.SourceType, &v.SourceURL, &v.S3Key, &v.Title, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	if pgErrorCode(err) == "23503" { // foreign_key_violation: user_id doesn't exist
		return models.Video{}, ErrUserNotFound
	}
	if err != nil {
		return models.Video{}, fmt.Errorf("create uploaded video: %w", err)
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

// SearchSimilarChunks returns the limit chunks of one video whose embeddings
// are closest (cosine distance) to queryEmbedding, closest first — the
// context retrieved for a Q&A request.
func (db *DB) SearchSimilarChunks(ctx context.Context, videoID string, queryEmbedding []float32, limit int) ([]models.Chunk, error) {
	rows, err := db.Query(ctx,
		`SELECT id, video_id, chunk_index, chunk_text, start_seconds, end_seconds, created_at
		 FROM chunks
		 WHERE video_id = $1
		 ORDER BY embedding <=> $2
		 LIMIT $3`,
		videoID, pgvector.NewVector(queryEmbedding), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search similar chunks: %w", err)
	}
	defer rows.Close()

	var chunks []models.Chunk
	for rows.Next() {
		var c models.Chunk
		if err := rows.Scan(&c.ID, &c.VideoID, &c.ChunkIndex, &c.ChunkText, &c.StartSeconds, &c.EndSeconds, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("search similar chunks: scan: %w", err)
		}
		chunks = append(chunks, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search similar chunks: %w", err)
	}
	return chunks, nil
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
