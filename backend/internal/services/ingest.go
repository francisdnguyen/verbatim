package services

import (
	"context"
	"fmt"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/models"
)

// IngestYouTubeVideo creates a new video row, then runs the full
// transcript → chunk → embed → store pipeline synchronously for it,
// updating its DB status at each stage so a caller polling the video row
// can see progress. The HTTP submit handler doesn't call this directly (it
// needs the row's ID back before the pipeline runs, so it calls CreateVideo
// and ProcessVideo itself) — this stays as the synchronous entry point used
// by the live integration test and any other non-HTTP caller.
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

	if err = ProcessVideo(ctx, db, transcriptClient, embedClient, video, videoURL, lang); err != nil {
		return video, err
	}
	video.Status = models.VideoStatusReady
	return video, nil
}

// ProcessVideo runs the transcript → chunk → embed → store pipeline for an
// already-created video row, updating its DB status at each stage. This is
// the body IngestYouTubeVideo runs synchronously, and the body an async
// caller (the HTTP submit handler) runs in a goroutine after creating the
// row itself, so it can return the row's ID to the client immediately.
func ProcessVideo(
	ctx context.Context,
	db *database.DB,
	transcriptClient *Client,
	embedClient *EmbeddingClient,
	video models.Video,
	videoURL, lang string,
) (err error) {
	// If anything below fails, best-effort mark the video failed rather than
	// leaving it stuck at pending/processing forever. If the failure-update
	// itself also fails, surface both errors instead of swallowing one.
	defer func() {
		if err != nil {
			if updateErr := db.UpdateVideoStatus(ctx, video.ID, models.VideoStatusFailed); updateErr != nil {
				err = fmt.Errorf("%w (also failed to mark video failed: %v)", err, updateErr)
			}
		}
	}()

	if err = db.UpdateVideoStatus(ctx, video.ID, models.VideoStatusProcessing); err != nil {
		return fmt.Errorf("process video: %w", err)
	}

	segments, err := transcriptClient.FetchTranscript(ctx, videoURL, lang)
	if err != nil {
		return fmt.Errorf("process video: fetch transcript: %w", err)
	}

	chunks := ChunkSegments(segments, 500, 50)
	if len(chunks) == 0 {
		return fmt.Errorf("process video: transcript produced no chunks")
	}

	chunkEmbeddings, err := embedClient.EmbedChunks(ctx, chunks)
	if err != nil {
		return fmt.Errorf("process video: embed chunks: %w", err)
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
		return fmt.Errorf("process video: %w", err)
	}

	if err = db.UpdateVideoStatus(ctx, video.ID, models.VideoStatusReady); err != nil {
		return fmt.Errorf("process video: %w", err)
	}

	return nil
}
