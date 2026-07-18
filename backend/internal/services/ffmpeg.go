package services

import (
	"context"
	"fmt"
	"os/exec"
)

// ExtractAudio shells out to the ffmpeg binary to pull the audio track out
// of inputPath (video or audio, any format ffmpeg reads) into outputPath as
// an MP3 — a uniform format Whisper accepts, regardless of what format the
// original upload came in as.
func ExtractAudio(ctx context.Context, inputPath, outputPath string) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("extract audio: ffmpeg not found in PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", inputPath,
		"-vn", // drop any video stream, audio only
		"-acodec", "libmp3lame",
		"-y", // overwrite outputPath if it already exists
		outputPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extract audio: ffmpeg: %w: %s", err, output)
	}
	return nil
}
