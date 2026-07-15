package services

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// ChunkEmbedding pairs a Chunk with its vector representation, ready for
// pgvector storage. float32 is pgvector's native precision, so the API's
// float64 values are converted here rather than left for a later step.
type ChunkEmbedding struct {
	Chunk     Chunk
	Embedding []float32
}

// EmbeddingClient talks to OpenAI's embeddings API.
type EmbeddingClient struct {
	client openai.Client
}

// NewEmbeddingClient builds an EmbeddingClient with the given API key. Extra
// opts (e.g. option.WithBaseURL) let tests point it at a mock server.
func NewEmbeddingClient(apiKey string, opts ...option.RequestOption) *EmbeddingClient {
	allOpts := append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)
	return &EmbeddingClient{client: openai.NewClient(allOpts...)}
}

// EmbedChunks embeds every chunk's text in a single batched API call, which
// is far cheaper and faster than one call per chunk for a realistic video's
// worth of chunks.
func (c *EmbeddingClient) EmbedChunks(ctx context.Context, chunks []Chunk) ([]ChunkEmbedding, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	texts := make([]string, len(chunks))
	for i, ch := range chunks {
		texts[i] = ch.Text
	}

	resp, err := c.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: openai.EmbeddingModelTextEmbedding3Small,
		Input: openai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
	})
	if err != nil {
		return nil, fmt.Errorf("embed chunks: %w", err)
	}
	if len(resp.Data) != len(chunks) {
		return nil, fmt.Errorf("embed chunks: expected %d embeddings, got %d", len(chunks), len(resp.Data))
	}

	// Place each embedding by its Index rather than trusting response array
	// order — the API returns them in input order, but Index turns that
	// into something we check instead of assume. filled catches a malformed
	// response reusing the same Index twice: without it, a duplicate would
	// silently overwrite a slot and leave another chunk's embedding zero-
	// valued instead of erroring — a chunk quietly vanishing from the index.
	embeddings := make([]ChunkEmbedding, len(chunks))
	filled := make([]bool, len(chunks))
	for _, e := range resp.Data {
		if e.Index < 0 || int(e.Index) >= len(chunks) {
			return nil, fmt.Errorf("embed chunks: response index %d out of range", e.Index)
		}
		if filled[e.Index] {
			return nil, fmt.Errorf("embed chunks: duplicate response index %d", e.Index)
		}
		filled[e.Index] = true
		embeddings[e.Index] = ChunkEmbedding{
			Chunk:     chunks[e.Index],
			Embedding: toFloat32(e.Embedding),
		}
	}

	return embeddings, nil
}

// toFloat32 converts the API's []float64 vector to pgvector's native
// []float32 precision.
func toFloat32(vec []float64) []float32 {
	out := make([]float32, len(vec))
	for i, v := range vec {
		out[i] = float32(v)
	}
	return out
}
