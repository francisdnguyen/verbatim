package services

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Client stores and retrieves uploaded video/audio files in one S3 bucket.
type S3Client struct {
	client *s3.Client
	bucket string
}

// NewS3Client builds an S3Client for the given bucket, using the AWS SDK's
// default credential chain (AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/
// AWS_REGION env vars) rather than custom credential plumbing.
func NewS3Client(ctx context.Context, bucket string) (*S3Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	return &S3Client{client: s3.NewFromConfig(cfg), bucket: bucket}, nil
}

// UploadObject stores body under key in the bucket.
func (c *S3Client) UploadObject(ctx context.Context, key string, body io.Reader, contentType string) error {
	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("upload object %s: %w", key, err)
	}
	return nil
}

// DeleteObject removes key from the bucket — used to clean up an upload
// that made it to S3 but failed to get a corresponding video row.
func (c *S3Client) DeleteObject(ctx context.Context, key string) error {
	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete object %s: %w", key, err)
	}
	return nil
}

// DownloadObjectToFile retrieves key from the bucket and writes it to
// destPath — a real local file, not an io.Reader, since ffmpeg (and Whisper)
// need a file path to operate on.
func (c *S3Client) DownloadObjectToFile(ctx context.Context, key, destPath string) error {
	resp, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("download object %s: %w", key, err)
	}
	defer resp.Body.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("download object %s: create %s: %w", key, destPath, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("download object %s: write %s: %w", key, destPath, err)
	}
	return nil
}
