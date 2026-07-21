# Verbatim
A Retrieval-Augmented Generation (RAG) platform for querying YouTube videos and uploaded video/audio files using natural language. Submit a video, ask a question, and get back an answer grounded in the actual transcript — with timestamp citations pointing to the exact moment it was said.

## Features
- YouTube ingestion: paste a URL, get an automatic transcript fetch and timestamped indexing
- Direct upload ingestion: upload video/audio files (up to 1 GiB), transcribed via ffmpeg + Whisper
- AI-powered Q&A: ask natural language questions, get GPT-4o answers with `[MM:SS-MM:SS]` timestamp citations
- Vector similarity search: pgvector-backed semantic search over transcript chunks
- Secure authentication: JWT-based auth with bcrypt password hashing and per-user video ownership checks
- Cloud-deployed: backend on AWS EC2 (Docker + Nginx + Let's Encrypt), frontend on Vercel

## Tech Stack

### Backend
- Go, native `net/http` (Go 1.22+ method+path routing, no router library)
- JWT authentication via `golang-jwt/jwt`, password hashing via `golang.org/x/crypto`'s bcrypt
- AWS SDK v2 for S3 access
- Official OpenAI Go client for embeddings, chat completions, and Whisper transcription
- Deployed as a Docker container on AWS EC2, behind Nginx with Let's Encrypt TLS

### Frontend
- React 19 + TypeScript
- React Router for navigation
- Tailwind CSS for styling
- Vite for build tooling
- Axios for API communication
- Deployed on Vercel

### Ingestion Pipeline
- Supadata API for YouTube transcript fetching (timestamped segments)
- ffmpeg for audio extraction from uploaded video/audio files
- OpenAI Whisper (`verbose_json`) for direct-upload transcription
- OpenAI `text-embedding-3-small` (1536 dimensions) for embeddings
- Chunking: ~500 words per chunk with 50-word overlap, timestamps preserved per chunk

### Database & Storage
- PostgreSQL 16 with the pgvector extension, on Amazon RDS
- IVFFlat index for vector similarity search
- Amazon S3 for uploaded video/audio files
