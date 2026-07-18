package services

import (
	"context"
	"fmt"
	"os"

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
	openAIClient *OpenAIClient,
	userID, videoURL, lang string,
) (video models.Video, err error) {
	video, err = db.CreateVideo(ctx, userID, videoURL)
	if err != nil {
		return models.Video{}, fmt.Errorf("ingest video: %w", err)
	}

	if err = ProcessVideo(ctx, db, transcriptClient, openAIClient, video, videoURL, lang); err != nil {
		return video, err
	}
	video.Status = models.VideoStatusReady
	return video, nil
}

// ProcessVideo runs the transcript → chunk → embed → store pipeline for an
// already-created YouTube video row, updating its DB status at each stage.
// This is the body IngestYouTubeVideo runs synchronously, and the body an
// async caller (the HTTP submit handler) runs in a goroutine after creating
// the row itself, so it can return the row's ID to the client immediately.
func ProcessVideo(
	ctx context.Context,
	db *database.DB,
	transcriptClient *Client,
	openAIClient *OpenAIClient,
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

	return finishIngestion(ctx, db, openAIClient, video, segments)
}

// ProcessUploadedVideo runs the download → extract audio → transcribe →
// chunk → embed → store pipeline for an already-created uploaded video row,
// updating its DB status at each stage. The upload counterpart to
// ProcessVideo: same failure-marking defer, same finishIngestion tail,
// differing only in how it obtains the transcript segments (Whisper over a
// downloaded file, rather than Supadata over a URL).
func ProcessUploadedVideo(
	ctx context.Context,
	db *database.DB,
	s3Client *S3Client,
	openAIClient *OpenAIClient,
	video models.Video,
	s3Key, lang string,
) (err error) {
	defer func() {
		if err != nil {
			if updateErr := db.UpdateVideoStatus(ctx, video.ID, models.VideoStatusFailed); updateErr != nil {
				err = fmt.Errorf("%w (also failed to mark video failed: %v)", err, updateErr)
			}
		}
	}()

	if err = db.UpdateVideoStatus(ctx, video.ID, models.VideoStatusProcessing); err != nil {
		return fmt.Errorf("process uploaded video: %w", err)
	}

	// Download to a local temp file first — ffmpeg operates on real files,
	// not S3 object streams. Removed once ffmpeg has read it.
	rawPath, err := tempFilePath("verbatim-upload-*")
	if err != nil {
		return fmt.Errorf("process uploaded video: %w", err)
	}
	defer os.Remove(rawPath)

	if err = s3Client.DownloadObjectToFile(ctx, s3Key, rawPath); err != nil {
		return fmt.Errorf("process uploaded video: %w", err)
	}

	// Extract to a uniform audio format regardless of the uploaded file's
	// original format (video or audio) — one pipeline, not a format matrix.
	audioPath, err := tempFilePath("verbatim-audio-*.mp3")
	if err != nil {
		return fmt.Errorf("process uploaded video: %w", err)
	}
	defer os.Remove(audioPath)

	if err = ExtractAudio(ctx, rawPath, audioPath); err != nil {
		return fmt.Errorf("process uploaded video: %w", err)
	}

	segments, err := openAIClient.TranscribeAudio(ctx, audioPath, lang)
	if err != nil {
		return fmt.Errorf("process uploaded video: %w", err)
	}

	return finishIngestion(ctx, db, openAIClient, video, segments)
}

// tempFilePath reserves a uniquely-named temp file (per the given pattern,
// as os.CreateTemp expects) and returns its path, closed and ready for
// another process (ffmpeg) or another writer (DownloadObjectToFile) to use.
func tempFilePath(pattern string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	path := f.Name()
	f.Close()
	return path, nil
}

// finishIngestion runs the chunk → embed → store tail shared by every
// ingestion source, once a video's transcript segments have been obtained
// — regardless of whether they came from Supadata captions or Whisper.
func finishIngestion(ctx context.Context, db *database.DB, openAIClient *OpenAIClient, video models.Video, segments []Segment) error {
	chunks := ChunkSegments(segments, 500, 50)
	if len(chunks) == 0 {
		return fmt.Errorf("finish ingestion: transcript produced no chunks")
	}

	chunkEmbeddings, err := openAIClient.EmbedChunks(ctx, chunks)
	if err != nil {
		return fmt.Errorf("finish ingestion: embed chunks: %w", err)
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

	if err := db.InsertChunks(ctx, modelChunks); err != nil {
		return fmt.Errorf("finish ingestion: %w", err)
	}

	if err := db.UpdateVideoStatus(ctx, video.ID, models.VideoStatusReady); err != nil {
		return fmt.Errorf("finish ingestion: %w", err)
	}

	return nil
}
