package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"verbatim/backend/internal/database"
	"verbatim/backend/internal/models"
)

// ErrVideoNotReady is returned when a question is asked about a video whose
// ingestion hasn't reached 'ready' yet (still pending/processing, or failed).
var ErrVideoNotReady = errors.New("video is not ready for questions")

// Source is one chunk retrieved as context for a question, with its
// timestamp range for citation — not necessarily one the model's answer
// actually drew on, just one it was given the chance to. Transient (built
// per-answer), not a DB row, so it lives here alongside Chunk/ChunkEmbedding
// rather than in models.
type Source struct {
	ChunkIndex   int     `json:"chunk_index"`
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	Text         string  `json:"chunk_text"`
}

// Answer is the result of AnswerQuestion: the model's response plus the
// chunks retrieved as context for it (see Source).
type Answer struct {
	Text    string
	Sources []Source
}

// topK is how many chunks are retrieved as context for one question. Fixed
// rather than a caller-supplied parameter until there's a real reason to
// tune it per request.
const topK = 5

// AnswerQuestion answers a question about one video: retrieves the top-k
// chunks most similar to the question, then asks GPT-4o to answer using
// only that context, citing the timestamp range of whatever it draws on.
func AnswerQuestion(
	ctx context.Context,
	db *database.DB,
	embedClient *EmbeddingClient,
	videoID, question string,
) (Answer, error) {
	video, err := db.GetVideo(ctx, videoID)
	if err != nil {
		return Answer{}, err
	}
	if video.Status != models.VideoStatusReady {
		return Answer{}, ErrVideoNotReady
	}

	queryEmbedding, err := embedClient.EmbedQuery(ctx, question)
	if err != nil {
		return Answer{}, fmt.Errorf("answer question: %w", err)
	}

	chunks, err := db.SearchSimilarChunks(ctx, videoID, queryEmbedding, topK)
	if err != nil {
		return Answer{}, fmt.Errorf("answer question: %w", err)
	}

	// Build the prompt's excerpt block and the response's Sources in the same
	// pass — every retrieved chunk becomes both a numbered excerpt the model
	// sees and a Source the caller sees, in the same order.
	var excerpts strings.Builder
	sources := make([]Source, len(chunks))
	for i, c := range chunks {
		fmt.Fprintf(&excerpts, "[%s-%s] %s\n\n", formatTimestamp(c.StartSeconds), formatTimestamp(c.EndSeconds), c.ChunkText)
		sources[i] = Source{ChunkIndex: c.ChunkIndex, StartSeconds: c.StartSeconds, EndSeconds: c.EndSeconds, Text: c.ChunkText}
	}

	systemPrompt := "You answer questions about a video using only the transcript excerpts provided. " +
		"Each excerpt is prefixed with its timestamp range like [MM:SS-MM:SS]. " +
		"When you use information from an excerpt, cite its timestamp range in your answer. " +
		"If the excerpts don't contain the answer, say so instead of guessing."
	userPrompt := fmt.Sprintf("Transcript excerpts:\n\n%s\nQuestion: %s", excerpts.String(), question)

	answerText, err := embedClient.Ask(ctx, systemPrompt, userPrompt)
	if err != nil {
		return Answer{}, fmt.Errorf("answer question: %w", err)
	}

	return Answer{Text: answerText, Sources: sources}, nil
}

// formatTimestamp renders seconds as MM:SS for citation in prompts.
func formatTimestamp(seconds float64) string {
	total := int(seconds)
	return fmt.Sprintf("%02d:%02d", total/60, total%60)
}
