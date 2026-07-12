CREATE EXTENSION IF NOT EXISTS vector;

-- Users table

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Videos table (tracks YouTube/upload ingestion and processing status)

CREATE TYPE video_source_type AS ENUM ('youtube', 'upload');
CREATE TYPE video_status AS ENUM ('pending', 'processing', 'ready', 'failed');

CREATE TABLE videos (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_type video_source_type NOT NULL,
    source_url  TEXT,  -- set when source_type = 'youtube'
    s3_key      TEXT,  -- set when source_type = 'upload'
    title       TEXT,
    status      video_status NOT NULL DEFAULT 'pending',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT video_source_check CHECK (
        (source_type = 'youtube' AND source_url IS NOT NULL) OR
        (source_type = 'upload'  AND s3_key IS NOT NULL)
    )
);

-- Create index for faster lookups
CREATE INDEX idx_videos_user_id ON videos(user_id);
CREATE INDEX idx_videos_status  ON videos(status);

-- Keep updated_at in sync automatically on every row update, so callers never
-- have to remember to set it themselves during a status transition
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_videos_updated_at
BEFORE UPDATE ON videos
FOR EACH ROW
EXECUTE FUNCTION set_updated_at();

-- Chunks table (stores transcript text chunks with embeddings)

CREATE TABLE chunks (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    video_id      UUID NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    chunk_index   INTEGER NOT NULL,
    chunk_text    TEXT NOT NULL,
    start_seconds DOUBLE PRECISION NOT NULL,
    end_seconds   DOUBLE PRECISION NOT NULL,
    embedding     vector(1536) NOT NULL,  -- OpenAI text-embedding-3-small is 1536 dimensions
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (video_id, chunk_index)
);

-- Create index for faster lookups
CREATE INDEX idx_chunks_video_id ON chunks(video_id);

-- Create index for fast vector similarity search
CREATE INDEX idx_chunks_embedding ON chunks
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
