package videocontext

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
)

const segmentMs = 60_000

// AudioToText is the ASR dependency of the transcriber.
type AudioToText interface {
	AudioToText(ctx context.Context, filePath string) (string, error)
}

// Transcriber cuts audio into fixed segments and transcribes them in parallel.
type Transcriber struct{ ASR AudioToText }

// FFmpegSegmentArgs is the audio segmentation command line.
func FFmpegSegmentArgs(video, outputPattern string) []string {
	return []string{"-y", "-i", video, "-vn", "-acodec", "libmp3lame",
		"-f", "segment", "-segment_time", "60", "-reset_timestamps", "1", outputPattern}
}

func runFFmpeg(ctx context.Context, args []string, timeoutMsg string) error {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	err := exec.CommandContext(cctx, "ffmpeg", args...).Run()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return common.Internal(timeoutMsg, nil)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return common.Internal("FFmpeg 执行失败", err)
	}
	return nil
}

// Transcribe cuts the audio into 60s mp3 files and transcribes each.
func (t *Transcriber) Transcribe(ctx context.Context, videoPath, audioDir string, counters Counters) ([]model.TranscriptSegment, error) {
	if err := os.MkdirAll(audioDir, 0o755); err != nil {
		return nil, err
	}
	if err := runFFmpeg(ctx, FFmpegSegmentArgs(videoPath, filepath.Join(audioDir, "audio_%03d.mp3")), "FFmpeg 执行超时"); err != nil {
		return nil, err
	}
	files, err := regularFiles(audioDir)
	if err != nil {
		return nil, err
	}
	result := []model.TranscriptSegment{}
	failed := 0
	var lastErr error
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		inc(counters, "asrCalls")
		text, err := t.ASR.AudioToText(ctx, f)
		if err != nil {
			failed++
			lastErr = err
			inc(counters, "asrSegmentFailures")
			slog.Warn("asr_segment_failed", "segment", i, "file", filepath.Base(f), "err", err)
			continue
		}
		if strings.TrimSpace(text) != "" {
			seg, err := model.NewTranscriptSegment(int64(i)*segmentMs, int64(i+1)*segmentMs, text)
			if err != nil {
				return nil, err
			}
			result = append(result, seg)
		}
	}
	if len(result) == 0 && failed > 0 {
		// keep the cause: 4xx (permanent) vs 429/5xx (retryable) must survive up to the consumer
		return nil, common.Internal("所有 ASR 分片均处理失败", lastErr)
	}
	return result, nil
}

func regularFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// TranscribeToText joins segment transcripts (used by /analysis/transcribe).
func (t *Transcriber) TranscribeToText(ctx context.Context, videoPath string) (string, error) {
	workDir := filepath.Join(os.TempDir(), "transcription-"+uuid.NewString())
	defer removeAll(workDir)
	segs, err := t.Transcribe(ctx, videoPath, workDir, nil)
	if err != nil {
		return "", common.Internal("视频转写失败", err)
	}
	var parts []string
	for _, s := range segs {
		if strings.TrimSpace(s.Text) != "" {
			parts = append(parts, s.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

func removeAll(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		slog.Warn("temporary_directory_cleanup_failed", "path", dir, "err", err)
	}
}

// TranscribeVideo transcribes a whole video into plain text.
func (t *Transcriber) TranscribeVideo(ctx context.Context, videoPath string) (string, error) {
	if strings.TrimSpace(videoPath) == "" {
		return "", common.InvalidArgument("视频路径为空")
	}
	if !strings.HasPrefix(videoPath, "http") {
		if st, err := os.Stat(videoPath); err != nil || !st.Mode().IsRegular() {
			return "", common.InvalidArgument("视频文件不存在")
		}
	}
	return t.TranscribeToText(ctx, videoPath)
}
