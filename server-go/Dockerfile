# syntax=docker/dockerfile:1
FROM golang:1.25 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags="-s -w" -o /out/server ./cmd/server

FROM debian:bookworm-slim
ARG TARGETARCH
# ffmpeg: audio segmentation + scene key frames; tesseract: OCR (-l chi_sim+eng); curl: healthcheck + yt-dlp download
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ffmpeg tesseract-ocr tesseract-ocr-chi-sim ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
# yt-dlp standalone binary (no Python needed)
RUN case "${TARGETARCH:-amd64}" in \
        arm64) YT=yt-dlp_linux_aarch64 ;; \
        *)     YT=yt-dlp_linux ;; \
    esac \
    && curl -fsSL "https://github.com/yt-dlp/yt-dlp/releases/latest/download/${YT}" -o /usr/local/bin/yt-dlp \
    && chmod 0755 /usr/local/bin/yt-dlp \
    && yt-dlp --version
RUN useradd --system --create-home --uid 10001 dovideo
COPY --from=build /out/server /usr/local/bin/server
USER dovideo
WORKDIR /home/dovideo
# the app defaults to loopback for local runs; a container must listen on all interfaces
ENV SERVER_ADDRESS=0.0.0.0 \
    SERVER_PORT=9090 \
    YTDLP_PATH=yt-dlp \
    OCR_COMMAND=tesseract
EXPOSE 9090
HEALTHCHECK --interval=15s --timeout=5s --start-period=30s --retries=5 \
    CMD curl -fsS "http://127.0.0.1:${SERVER_PORT:-9090}/health" || exit 1
ENTRYPOINT ["/usr/local/bin/server"]
