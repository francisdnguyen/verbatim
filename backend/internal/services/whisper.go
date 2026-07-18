package services

import (
	"context"
	"fmt"
	"os"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
)

// TranscribeAudio transcribes a local audio file with OpenAI's Whisper
// model, returning timestamped segments — the same shape FetchTranscript
// returns for YouTube captions, so callers downstream of either source
// (chunking, in particular) don't need to know which one produced them.
// lang is an optional ISO 639-1 code (e.g. "en"); pass "" to let Whisper
// auto-detect. Supplying it avoids the same wrong-language auto-detection
// FetchTranscript hit with Supadata for an unspecified language.
func (c *OpenAIClient) TranscribeAudio(ctx context.Context, audioFilePath, lang string) ([]Segment, error) {
	file, err := os.Open(audioFilePath)
	if err != nil {
		return nil, fmt.Errorf("transcribe audio: open file: %w", err)
	}
	defer file.Close()

	params := openai.AudioTranscriptionNewParams{
		File:           file,
		Model:          openai.AudioModelWhisper1,
		ResponseFormat: openai.AudioResponseFormatVerboseJSON,
	}
	if lang != "" {
		params.Language = param.NewOpt(lang)
	}

	resp, err := c.client.Audio.Transcriptions.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("transcribe audio: %w", err)
	}

	segments := make([]Segment, len(resp.Segments))
	for i, s := range resp.Segments {
		segments[i] = Segment{Text: s.Text, StartSeconds: s.Start, EndSeconds: s.End}
	}
	return segments, nil
}
