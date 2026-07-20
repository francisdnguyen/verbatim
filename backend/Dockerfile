# Build stage: compile a static Linux binary
FROM golang:1.26-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO disabled: pgx/aws-sdk/openai-go are all pure Go, so a static binary
# needs no glibc from the runtime image below.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/server ./cmd/api

# Runtime stage: slim image with ffmpeg installed (the app's one external
# system dependency, per CLAUDE.md), since ffmpeg.go shells out expecting
# it on PATH.
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ffmpeg ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --no-create-home appuser

COPY --from=builder /out/server /usr/local/bin/server

# Don't run the server as root; os.CreateTemp's default dir (/tmp) stays
# writable by a non-root user, so the ffmpeg/Whisper temp-file path still works.
USER appuser

EXPOSE 8080

CMD ["server"]
