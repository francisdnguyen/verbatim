package services

import (
	"context"
	"fmt"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/models"
)

// IngestYouTubeVideo runs the full transcript → chunk → embed → store
// pipeline synchronously for one YouTube video, updating its DB status at
// each stage so a caller polling the video row can see progress.
func IngestYouTubeVideo(
	ctx context.Context,
	db *database.DB,
	transcriptClient *Client,
	embedClient *EmbeddingClient,
	userID, videoURL, lang string,
) (video models.Video, err error) {
	video, err = db.CreateVideo(ctx, userID, videoURL)
	if err != nil {
		return models.Video{}, fmt.Errorf("ingest video: %w", err)
	}

	// If anything below fails, best-effort mark the video failed rather than
	// leaving it stuck at pending/processing forever. If the failure-update
	// itself also fails, surface both errors instead of swallowing one.
	// Critical: every error return below this point must return `video`
	// (which carries the real ID), never a fresh models.Video{} — the zero
	// value would overwrite the named return before this defer runs, so
	// UpdateVideoStatus would be called with an empty ID and silently fail
	// to mark anything, leaving the row stuck at its previous status forever.
	defer func() {
		if err != nil {
			if updateErr := db.UpdateVideoStatus(ctx, video.ID, models.VideoStatusFailed); updateErr != nil {
				err = fmt.Errorf("%w (also failed to mark video failed: %v)", err, updateErr)
			}
		}
	}()

	if err = db.UpdateVideoStatus(ctx, video.ID, models.VideoStatusProcessing); err != nil {
		return video, fmt.Errorf("ingest video: %w", err)
	}

	segments, err := transcriptClient.FetchTranscript(ctx, videoURL, lang)
	if err != nil {
		return video, fmt.Errorf("ingest video: fetch transcript: %w", err)
	}

	chunks := ChunkSegments(segments, 500, 50)
	if len(chunks) == 0 {
		err = fmt.Errorf("ingest video: transcript produced no chunks")
		return video, err
	}

	chunkEmbeddings, err := embedClient.EmbedChunks(ctx, chunks)
	if err != nil {
		return video, fmt.Errorf("ingest video: embed chunks: %w", err)
	}

	// Map from the in-memory services.Chunk shape to the DB-row models.Chunk
	// shape (Text -> ChunkText, plus stamping VideoID) right at this boundary.
	modelChunks := make([]models.Chunk, len(chunkEmbeddings))
	for i, ce := range chunkEmbeddings {
		modelChunks[i] = models.Chunk{
			VideoID:      video.ID,
			ChunkIndex:   ce.Chunk.ChunkIndex,
			ChunkText:    ce.Chunk.Text,
			StartSeconds: ce.Chunk.StartSeconds,
			EndSeconds:   ce.Chunk.EndSeconds,
			Embedding:    ce.Embedding,
		}
	}

	if err = db.InsertChunks(ctx, modelChunks); err != nil {
		return video, fmt.Errorf("ingest video: %w", err)
	}

	if err = db.UpdateVideoStatus(ctx, video.ID, models.VideoStatusReady); err != nil {
		return video, fmt.Errorf("ingest video: %w", err)
	}
	video.Status = models.VideoStatusReady

	return video, nil
}
