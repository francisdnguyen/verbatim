package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// defaultBaseURL is the Supadata API root; overridable per-Client so tests can
// point at an httptest server instead of the live API.
const defaultBaseURL = "https://api.supadata.ai/v1"

// ErrTranscriptUnavailable is returned when Supadata responds 206, meaning the
// video exists but has no captions to transcribe.
var ErrTranscriptUnavailable = errors.New("transcript unavailable for video")

// ErrTranscriptPending is returned when Supadata responds 202, meaning the
// transcript is being generated asynchronously (long video / AI generation).
// Polling the returned job isn't implemented yet — that belongs with the
// async ingestion pipeline — so callers just get told "not ready" for now.
var ErrTranscriptPending = errors.New("transcript is being generated asynchronously")

// Segment is one timestamped piece of a transcript, with times in seconds.
// It's transient (feeds chunking later), not a DB row, so it lives here.
type Segment struct {
	Text         string
	StartSeconds float64
	EndSeconds   float64
}

// Client talks to the Supadata YouTube transcript API.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// NewClient builds a Supadata client with the given API key and sane defaults.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:     apiKey,
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// supadataResponse mirrors the JSON shape returned when text=false: a list of
// timestamped chunks. offset/duration are in milliseconds.
type supadataResponse struct {
	Content []struct {
		Text     string `json:"text"`
		Offset   int64  `json:"offset"`   // start time, milliseconds
		Duration int64  `json:"duration"` // length, milliseconds
		Lang     string `json:"lang"`
	} `json:"content"`
	Lang string `json:"lang"`
}

// FetchTranscript retrieves the timestamped transcript for a YouTube video URL.
// lang is an optional ISO 639-1 code (e.g. "en") requesting that language's
// captions; pass "" to let Supadata pick its own default.
func (c *Client) FetchTranscript(ctx context.Context, videoURL, lang string) ([]Segment, error) {
	// text=false asks for timestamped chunks rather than one plain-text blob.
	q := url.Values{}
	q.Set("url", videoURL)
	q.Set("text", "false")
	if lang != "" {
		q.Set("lang", lang)
	}
	endpoint := c.baseURL + "/youtube/transcript?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("x-api-key", c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call supadata: %w", err)
	}
	defer resp.Body.Close()

	// 206 is Supadata's "no captions" signal — surface it as a typed error so
	// callers can treat a caption-less video differently from a real failure.
	if resp.StatusCode == http.StatusPartialContent {
		return nil, ErrTranscriptUnavailable
	}
	// 202 means Supadata is generating the transcript asynchronously (e.g. a
	// long video with no native captions) and returns a jobId to poll later.
	if resp.StatusCode == http.StatusAccepted {
		var pending struct {
			JobID string `json:"jobId"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&pending) // best-effort; jobId is informational only for now
		return nil, fmt.Errorf("%w: job %s", ErrTranscriptPending, pending.JobID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("supadata returned %d: %s", resp.StatusCode, body)
	}

	var parsed supadataResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	// Convert each chunk's millisecond times into the seconds our schema uses.
	segments := make([]Segment, 0, len(parsed.Content))
	for _, seg := range parsed.Content {
		segments = append(segments, Segment{
			Text:         seg.Text,
			StartSeconds: float64(seg.Offset) / 1000.0,
			EndSeconds:   float64(seg.Offset+seg.Duration) / 1000.0,
		})
	}

	return segments, nil
}
