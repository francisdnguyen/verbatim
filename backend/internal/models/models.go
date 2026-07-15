package models

import "time"

// User represents a row in the users table
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// VideoSourceType identifies where a video came from
type VideoSourceType string

const (
	VideoSourceYouTube VideoSourceType = "youtube"
	VideoSourceUpload  VideoSourceType = "upload"
)

// VideoStatus tracks ingestion progress
type VideoStatus string

const (
	VideoStatusPending    VideoStatus = "pending"
	VideoStatusProcessing VideoStatus = "processing"
	VideoStatusReady      VideoStatus = "ready"
	VideoStatusFailed     VideoStatus = "failed"
)

// Video represents a row in the videos table
type Video struct {
	ID         string          `json:"id"`
	UserID     string          `json:"user_id"`
	SourceType VideoSourceType `json:"source_type"`
	SourceURL  *string         `json:"source_url,omitempty"`
	S3Key      *string         `json:"s3_key,omitempty"`
	Title      *string         `json:"title,omitempty"`
	Status     VideoStatus     `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// Chunk represents a row in the chunks table
type Chunk struct {
	ID           string    `json:"id"`
	VideoID      string    `json:"video_id"`
	ChunkIndex   int       `json:"chunk_index"`
	ChunkText    string    `json:"chunk_text"`
	StartSeconds float64   `json:"start_seconds"`
	EndSeconds   float64   `json:"end_seconds"`
	Embedding    []float32 `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}
