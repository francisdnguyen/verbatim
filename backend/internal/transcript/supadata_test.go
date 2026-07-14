package transcript

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

// liveTestVideo is a stable, captioned YouTube video used by the live test.
// Swap it if it ever goes away. ("Me at the zoo" — the first YouTube video.)
const liveTestVideo = "https://www.youtube.com/watch?v=jNQXAC9IVRw"

// newTestClient returns a Client pointed at the given test server URL.
func newTestClient(baseURL string) *Client {
	return &Client{
		apiKey:     "test-key",
		baseURL:    baseURL,
		httpClient: http.DefaultClient,
	}
}

// Verifies parsing, ms→s conversion, and that the API key header is sent.
func TestFetchTranscript_ParsesAndConverts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assert inside the handler so there's no cross-goroutine shared state.
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("expected x-api-key header 'test-key', got %q", got)
		}
		// text=false is what asks for timestamped chunks — confirm we send it.
		if r.URL.Query().Get("text") != "false" {
			t.Errorf("expected text=false, got %q", r.URL.Query().Get("text"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"content": [
				{"text": "hello", "offset": 1500, "duration": 3000, "lang": "en"},
				{"text": "world", "offset": 4500, "duration": 1000, "lang": "en"}
			],
			"lang": "en"
		}`))
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	segments, err := client.FetchTranscript(context.Background(), "https://youtu.be/abc123", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}

	// 1500ms → 1.5s start, (1500+3000)ms → 4.5s end.
	if segments[0].Text != "hello" || segments[0].StartSeconds != 1.5 || segments[0].EndSeconds != 4.5 {
		t.Errorf("segment[0] wrong: %+v", segments[0])
	}
	// 4500ms → 4.5s start, (4500+1000)ms → 5.5s end.
	if segments[1].Text != "world" || segments[1].StartSeconds != 4.5 || segments[1].EndSeconds != 5.5 {
		t.Errorf("segment[1] wrong: %+v", segments[1])
	}
}

// A 206 (no captions) should surface as the typed ErrTranscriptUnavailable.
func TestFetchTranscript_NoCaptions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	_, err := client.FetchTranscript(context.Background(), "https://youtu.be/nocaptions", "")
	if !errors.Is(err, ErrTranscriptUnavailable) {
		t.Fatalf("expected ErrTranscriptUnavailable, got %v", err)
	}
}

// A 202 (still generating) should surface as the typed ErrTranscriptPending.
func TestFetchTranscript_Pending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"jobId": "job-123"}`))
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	_, err := client.FetchTranscript(context.Background(), "https://youtu.be/longvideo", "")
	if !errors.Is(err, ErrTranscriptPending) {
		t.Fatalf("expected ErrTranscriptPending, got %v", err)
	}
	if !strings.Contains(err.Error(), "job-123") {
		t.Errorf("expected error to mention the job id, got %q", err.Error())
	}
}

// The lang parameter, when given, should be forwarded on the request.
func TestFetchTranscript_SendsLangWhenGiven(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("lang"); got != "en" {
			t.Errorf("expected lang=en, got %q", got)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"content": [], "lang": "en"}`))
	}))
	defer srv.Close()

	client := newTestClient(srv.URL)
	if _, err := client.FetchTranscript(context.Background(), "https://youtu.be/abc123", "en"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestFetchTranscript_Live hits the real Supadata API. It skips unless a
// SUPADATA_API_KEY is available, so CI (which has no key) skips it cleanly.
func TestFetchTranscript_Live(t *testing.T) {
	// Best-effort: pull the key from backend/.env when running locally.
	_ = godotenv.Load("../../.env")

	apiKey := os.Getenv("SUPADATA_API_KEY")
	if apiKey == "" {
		t.Skip("SUPADATA_API_KEY not set; skipping live API test")
	}

	client := NewClient(apiKey)
	// lang=en pinned so the test is deterministic — without it Supadata may
	// return whatever language it defaults to (observed: German, once).
	segments, err := client.FetchTranscript(context.Background(), liveTestVideo, "en")
	if err != nil {
		t.Fatalf("live fetch failed: %v", err)
	}
	if len(segments) == 0 {
		t.Fatal("expected at least one segment from a captioned video")
	}

	// Sanity-check the shape of real data: non-empty text, sensible timestamps.
	for i, s := range segments {
		if s.Text == "" {
			t.Errorf("segment[%d] has empty text", i)
		}
		if s.StartSeconds < 0 || s.EndSeconds < s.StartSeconds {
			t.Errorf("segment[%d] has bad timing: start=%v end=%v", i, s.StartSeconds, s.EndSeconds)
		}
	}

	// Print a few real segments so `-v` runs show what came back.
	for i := 0; i < 3 && i < len(segments); i++ {
		t.Logf("segment[%d]: [%.2fs-%.2fs] %q", i, segments[i].StartSeconds, segments[i].EndSeconds, segments[i].Text)
	}
}
