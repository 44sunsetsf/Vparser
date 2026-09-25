// Package videocontext builds the multimodal VideoContext of a video: an ASR branch (segmented audio
// transcription) and a vision branch (scene-change key frames, perceptual-hash dedup, OCR) run in
// parallel on bounded pools and are merged into 60-second segments. Either branch may fail alone;
// the context is still useful with speech only or on-screen text only.
package videocontext

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/obs"
	"dovideo/server/internal/workerpool"
)

const (
	evidenceObjectPrefix    = "evidence-frames"
	fallbackFrameIntervalMs = 30_000
	totalBudget             = 60 * time.Minute
	hashSkipDistance        = 5
)

// Store is the MinIO subset used for evidence frames.
type Store interface {
	ReadableSource(ctx context.Context, source string) (string, error)
	UploadLocalFile(ctx context.Context, path, originalFilename, objectPrefix string) (string, error)
	RemoveFile(ctx context.Context, fileURL string) error
	IsManagedFileWithPrefix(fileURL, objectPrefix string) bool
}

// OCRRunner recognises text in an image.
type OCRRunner interface {
	Recognize(ctx context.Context, image string) (string, error)
}

// Service builds VideoContext values.
type Service struct {
	Transcriber *Transcriber
	OCR         OCRRunner
	Store       Store
	ASRPool     *workerpool.Pool
	OCRPool     *workerpool.Pool
}

type framePart struct {
	timestampMs int64
	ocrText     string
	frameName   string
}

type branch[T any] struct {
	items []T
	err   error
}

func submitBranch[T any](pool *workerpool.Pool, wg *sync.WaitGroup, work func() ([]T, error)) <-chan branch[T] {
	ch := make(chan branch[T], 1)
	wg.Add(1)
	err := pool.Submit(func() {
		defer wg.Done()
		items, err := work()
		if err != nil {
			ch <- branch[T]{err: err}
			return
		}
		ch <- branch[T]{items: items}
	})
	if err != nil {
		wg.Done()
		ch <- branch[T]{err: err}
	}
	return ch
}

// Build produces the context of videoPath; both branches share a total time budget.
func (s *Service) Build(ctx context.Context, videoPath, userGoal string, counters Counters) (model.VideoContext, error) {
	readable, err := s.Store.ReadableSource(ctx, videoPath)
	if err != nil {
		return model.VideoContext{}, common.Internal("VideoContext 构建失败", err)
	}
	workDir := filepath.Join(os.TempDir(), "video-context-"+uuid.NewString())
	var uploadedMu sync.Mutex
	var uploaded []string
	cleanupWorkDir := true
	defer func() {
		if cleanupWorkDir {
			removeAll(workDir)
		} else {
			slog.Warn("video_context_workdir_retained", "path", workDir, "reason", "branch_still_running")
		}
	}()
	fail := func(err error) (model.VideoContext, error) {
		s.deleteFrames(ctx, uploaded)
		return model.VideoContext{}, common.Internal("VideoContext 构建失败", err)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fail(err)
	}

	bctx, cancel := context.WithTimeout(ctx, totalBudget)
	defer cancel()
	var wg sync.WaitGroup
	tCh := submitBranch(s.ASRPool, &wg, func() ([]model.TranscriptSegment, error) {
		ctx, span := obs.Tracer().Start(bctx, "video_context.asr")
		defer span.End()
		segs, err := s.Transcriber.Transcribe(ctx, readable, filepath.Join(workDir, "audio"), counters)
		endSpan(span, err, attribute.Int("asr.segments", len(segs)))
		return segs, err
	})
	fCh := submitBranch(s.OCRPool, &wg, func() ([]framePart, error) {
		ctx, span := obs.Tracer().Start(bctx, "video_context.frames_ocr")
		defer span.End()
		frames, err := s.extractKeyFrames(ctx, readable, filepath.Join(workDir, "frames"), counters, func(u string) {
			uploadedMu.Lock()
			uploaded = append(uploaded, u)
			uploadedMu.Unlock()
		})
		endSpan(span, err, attribute.Int("ocr.frames", len(frames)))
		return frames, err
	})
	cancelBranches := func() bool {
		cancel()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
			return true
		case <-time.After(10 * time.Second):
			return false
		}
	}
	var tRes branch[model.TranscriptSegment]
	var fRes branch[framePart]
	select {
	case tRes = <-tCh:
	case <-bctx.Done():
		cleanupWorkDir = cancelBranches()
		return fail(common.Internal("VideoContext 分支处理超过总时间预算", bctx.Err()))
	}
	select {
	case fRes = <-fCh:
	case <-bctx.Done():
		cleanupWorkDir = cancelBranches()
		return fail(common.Internal("VideoContext 分支处理超过总时间预算", bctx.Err()))
	}
	vc, err := s.finish(ctx, videoPath, userGoal, counters, tRes, fRes, &uploadedMu, &uploaded)
	if err != nil {
		return fail(err)
	}
	return vc, nil
}

func (s *Service) finish(ctx context.Context, videoPath, userGoal string, counters Counters,
	t branch[model.TranscriptSegment], f branch[framePart], mu *sync.Mutex, uploaded *[]string) (model.VideoContext, error) {
	if t.err != nil && f.err != nil {
		return model.VideoContext{}, common.Internal("ASR 和 OCR 分支均失败", t.err)
	}
	if t.err != nil {
		inc(counters, "asrBranchFailures")
		slog.Warn("video_context_asr_branch_failed", "err", t.err)
	}
	if f.err != nil {
		inc(counters, "ocrBranchFailures")
		slog.Warn("video_context_ocr_branch_failed", "err", f.err)
		mu.Lock()
		s.deleteFrames(ctx, *uploaded)
		*uploaded = nil
		mu.Unlock()
	}
	segments, err := merge(t.items, f.items)
	if err != nil {
		return model.VideoContext{}, err
	}
	if len(segments) == 0 {
		return model.VideoContext{}, common.Internal("视频未解析出有效语音或画面文字", nil)
	}
	return model.NewVideoContext(videoPath, userGoal, segments)
}

func (s *Service) extractKeyFrames(ctx context.Context, videoPath, frameDir string, counters Counters,
	onUpload func(string)) ([]framePart, error) {
	if err := os.MkdirAll(frameDir, 0o755); err != nil {
		return nil, err
	}
	timestamps, err := runFrameCommand(ctx, FFmpegFrameArgs(videoPath, frameDir, FrameGapSeconds(probeDuration(ctx, videoPath))))
	if err != nil {
		return nil, err
	}
	files, err := regularFiles(frameDir)
	if err != nil {
		return nil, err
	}
	result := []framePart{}
	var prev *uint64
	failedFrames := 0
	for i, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hash := DifferenceHash(file)
		if prev != nil && HammingDistance(*prev, hash) <= hashSkipDistance {
			continue
		}
		h := hash
		prev = &h
		ts := int64(i) * fallbackFrameIntervalMs
		if i < len(timestamps) {
			ts = timestamps[i]
		}
		inc(counters, "ocrCalls")
		ocrText, err := s.OCR.Recognize(ctx, file)
		if err != nil {
			failedFrames++
			inc(counters, "ocrFrameFailures")
			slog.Warn("ocr_frame_failed", "frame", filepath.Base(file), "timestampMs", ts, "err", err)
			continue
		}
		frameURL, err := s.Store.UploadLocalFile(ctx, file, filepath.Base(file), evidenceObjectPrefix)
		if err != nil {
			inc(counters, "frameUploadFailures")
			slog.Warn("evidence_frame_upload_failed", "frame", filepath.Base(file), "timestampMs", ts, "err", err)
			frameURL = videoPath + "#timestampMs=" + strconv.FormatInt(ts, 10)
		} else {
			onUpload(frameURL)
		}
		result = append(result, framePart{timestampMs: ts, ocrText: ocrText, frameName: frameURL})
	}
	if len(result) == 0 && failedFrames > 0 {
		return nil, common.Internal("所有 OCR 关键帧均处理失败", nil)
	}
	return result, nil
}

type segmentBuilder struct {
	transcripts, ocrTexts, frames []string
}

func windowStart(ts int64) int64 { return ts / segmentMs * segmentMs }

func merge(transcripts []model.TranscriptSegment, frames []framePart) ([]model.VideoSegment, error) {
	windows := map[int64]*segmentBuilder{}
	get := func(k int64) *segmentBuilder {
		if windows[k] == nil {
			windows[k] = &segmentBuilder{}
		}
		return windows[k]
	}
	for _, t := range transcripts {
		b := get(windowStart(t.StartMs))
		b.transcripts = append(b.transcripts, t.Text)
	}
	for _, f := range frames {
		b := get(windowStart(f.timestampMs))
		if strings.TrimSpace(f.ocrText) != "" {
			b.ocrTexts = append(b.ocrTexts, f.ocrText)
		}
		b.frames = append(b.frames, f.frameName)
	}
	keys := make([]int64, 0, len(windows))
	for k := range windows {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	out := make([]model.VideoSegment, 0, len(keys))
	for _, k := range keys {
		b := windows[k]
		seg, err := model.NewVideoSegment(k, k+segmentMs, strings.Join(b.transcripts, "\n"), b.ocrTexts, b.frames)
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, nil
}

// DeleteEvidenceFrames removes managed evidence-frame objects referenced by the context.
func (s *Service) DeleteEvidenceFrames(ctx context.Context, vc *model.VideoContext) {
	if vc == nil {
		return
	}
	seen := map[string]bool{}
	var frames []string
	for _, seg := range vc.Segments {
		for _, f := range seg.EvidenceFrames {
			if !seen[f] {
				seen[f] = true
				frames = append(frames, f)
			}
		}
	}
	s.deleteFrames(ctx, frames)
}

func (s *Service) deleteFrames(ctx context.Context, frames []string) {
	seen := map[string]bool{}
	for _, f := range frames {
		if seen[f] || !s.Store.IsManagedFileWithPrefix(f, evidenceObjectPrefix) {
			continue
		}
		seen[f] = true
		if err := s.Store.RemoveFile(context.WithoutCancel(ctx), f); err != nil {
			slog.Warn("evidence_frame_cleanup_failed", "frame", f, "err", err)
		}
	}
}

// endSpan annotates a branch span with its result size or error.
func endSpan(span trace.Span, err error, result attribute.KeyValue) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "branch failed")
		return
	}
	span.SetAttributes(result)
}
