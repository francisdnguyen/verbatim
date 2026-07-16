package services

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

// ChunkEmbedding pairs a Chunk with its vector representation, ready for
// pgvector storage. float32 is pgvector's native precision, so the API's
// float64 values are converted here rather than left for a later step.
type ChunkEmbedding struct {
	Chunk     Chunk
	Embedding []float32
}

// EmbeddingClient talks to OpenAI's embeddings and chat completions APIs.
// Both live on one client (they're the same underlying openai.Client/API
// key) rather than a separate wrapper per capability.
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

// EmbedQuery embeds a single piece of text (e.g. a user's question) rather
// than a batch of chunks, for use at query time.
func (c *EmbeddingClient) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	resp, err := c.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: openai.EmbeddingModelTextEmbedding3Small,
		Input: openai.EmbeddingNewParamsInputUnion{OfString: param.NewOpt(text)},
	})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(resp.Data) != 1 {
		return nil, fmt.Errorf("embed query: expected 1 embedding, got %d", len(resp.Data))
	}
	return toFloat32(resp.Data[0].Embedding), nil
}

// Ask sends a system/user prompt pair to GPT-4o and returns the model's
// response text.
func (c *EmbeddingClient) Ask(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	resp, err := c.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModelGPT4o,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userPrompt),
		},
	})
	if err != nil {
		return "", fmt.Errorf("chat completion: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("chat completion returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
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
