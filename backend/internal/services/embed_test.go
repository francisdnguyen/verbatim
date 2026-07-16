package services

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/joho/godotenv"
	"github.com/openai/openai-go/v3/option"
)

// Verifies the request is batched (all chunk texts in one call) and the
// response maps back to the right Chunk, in order, with the API's float64
// vector correctly converted to float32. Values are chosen to be exactly
// representable in both float32 and float64, so the comparison can't be
// flaky from double-rounding.
func TestEmbedChunks_ParsesAndConverts(t *testing.T) {
	chunks := []Chunk{
		{Text: "chunk one text", ChunkIndex: 0},
		{Text: "chunk two text", ChunkIndex: 1},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if len(body.Input) != 2 || body.Input[0] != "chunk one text" || body.Input[1] != "chunk two text" {
			t.Errorf("expected both chunk texts batched in one request, got %v", body.Input)
		}
		if body.Model != "text-embedding-3-small" {
			t.Errorf("expected model text-embedding-3-small, got %q", body.Model)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"data": [
				{"embedding": [0.5, -0.25, 1.0], "index": 0, "object": "embedding"},
				{"embedding": [0.75, 2.0, -0.5], "index": 1, "object": "embedding"}
			],
			"model": "text-embedding-3-small",
			"object": "list",
			"usage": {"prompt_tokens": 10, "total_tokens": 10}
		}`))
	}))
	defer srv.Close()

	client := NewEmbeddingClient("test-key", option.WithBaseURL(srv.URL))
	results, err := client.EmbedChunks(context.Background(), chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].Chunk != chunks[0] {
		t.Errorf("result[0].Chunk = %+v, want %+v", results[0].Chunk, chunks[0])
	}
	wantVec0 := []float32{0.5, -0.25, 1.0}
	if !floatsEqual(results[0].Embedding, wantVec0) {
		t.Errorf("result[0].Embedding = %v, want %v", results[0].Embedding, wantVec0)
	}

	if results[1].Chunk != chunks[1] {
		t.Errorf("result[1].Chunk = %+v, want %+v", results[1].Chunk, chunks[1])
	}
	wantVec1 := []float32{0.75, 2.0, -0.5}
	if !floatsEqual(results[1].Embedding, wantVec1) {
		t.Errorf("result[1].Embedding = %v, want %v", results[1].Embedding, wantVec1)
	}
}

// Empty input should produce no results and no API call.
func TestEmbedChunks_EmptyInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no request should be made for empty input")
	}))
	defer srv.Close()

	client := NewEmbeddingClient("test-key", option.WithBaseURL(srv.URL))
	results, err := client.EmbedChunks(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no results, got %d", len(results))
	}
}

// Response entries arriving out of array order should still land in the
// right slot — placement is Index-driven, not array-position-driven.
func TestEmbedChunks_HandlesOutOfOrderResponse(t *testing.T) {
	chunks := []Chunk{
		{Text: "chunk zero", ChunkIndex: 0},
		{Text: "chunk one", ChunkIndex: 1},
		{Text: "chunk two", ChunkIndex: 2},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Deliberately out of order: index 2, then 0, then 1.
		w.Write([]byte(`{
			"data": [
				{"embedding": [2.0], "index": 2, "object": "embedding"},
				{"embedding": [0.0], "index": 0, "object": "embedding"},
				{"embedding": [1.0], "index": 1, "object": "embedding"}
			],
			"model": "text-embedding-3-small",
			"object": "list",
			"usage": {"prompt_tokens": 3, "total_tokens": 3}
		}`))
	}))
	defer srv.Close()

	client := NewEmbeddingClient("test-key", option.WithBaseURL(srv.URL))
	results, err := client.EmbedChunks(context.Background(), chunks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, want := range []float32{0.0, 1.0, 2.0} {
		if !floatsEqual(results[i].Embedding, []float32{want}) {
			t.Errorf("results[%d].Embedding = %v, want [%v]", i, results[i].Embedding, want)
		}
		if results[i].Chunk != chunks[i] {
			t.Errorf("results[%d].Chunk = %+v, want %+v", i, results[i].Chunk, chunks[i])
		}
	}
}

// A response reusing the same Index twice must error, not silently corrupt
// results — without this check, one chunk's embedding would overwrite
// another's slot and a different chunk would be left zero-valued.
func TestEmbedChunks_RejectsDuplicateIndex(t *testing.T) {
	chunks := []Chunk{
		{Text: "chunk zero", ChunkIndex: 0},
		{Text: "chunk one", ChunkIndex: 1},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Both entries claim index 0; index 1 is never present.
		w.Write([]byte(`{
			"data": [
				{"embedding": [0.0], "index": 0, "object": "embedding"},
				{"embedding": [1.0], "index": 0, "object": "embedding"}
			],
			"model": "text-embedding-3-small",
			"object": "list",
			"usage": {"prompt_tokens": 2, "total_tokens": 2}
		}`))
	}))
	defer srv.Close()

	client := NewEmbeddingClient("test-key", option.WithBaseURL(srv.URL))
	_, err := client.EmbedChunks(context.Background(), chunks)
	if err == nil {
		t.Fatal("expected an error for a duplicate response index, got nil")
	}
}

// TestEmbedChunks_Live hits the real OpenAI API. It skips unless an
// OPENAI_API_KEY is available, so CI (which has no key) skips it cleanly.
func TestEmbedChunks_Live(t *testing.T) {
	_ = godotenv.Load("../../.env")

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set; skipping live API test")
	}

	chunks := []Chunk{
		{Text: "a video about elephants", ChunkIndex: 0},
		{Text: "a recipe for chocolate cake", ChunkIndex: 1},
	}

	client := NewEmbeddingClient(apiKey)
	results, err := client.EmbedChunks(context.Background(), chunks)
	if err != nil {
		t.Fatalf("live embed failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(results))
	}

	for i, r := range results {
		if len(r.Embedding) != 1536 {
			t.Errorf("result[%d]: expected 1536 dimensions, got %d", i, len(r.Embedding))
		}
		if isAllZero(r.Embedding) {
			t.Errorf("result[%d]: embedding is all-zero, looks like a stub response", i)
		}
	}

	// The real sanity check: two unrelated texts should produce meaningfully
	// different embeddings, not near-identical (or identical) vectors.
	sim := cosineSimilarity(results[0].Embedding, results[1].Embedding)
	t.Logf("cosine similarity between unrelated texts: %.4f", sim)
	if sim > 0.7 {
		t.Errorf("expected unrelated texts to have low similarity, got %.4f", sim)
	}
}

func floatsEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func isAllZero(v []float32) bool {
	for _, x := range v {
		if x != 0 {
			return false
		}
	}
	return true
}

func cosineSimilarity(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
