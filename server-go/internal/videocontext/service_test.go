package videocontext

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/workerpool"
)

func TestMergeWindows(t *testing.T) {
	ts, _ := model.NewTranscriptSegment(0, 60000, "hello")
	ts2, _ := model.NewTranscriptSegment(120000, 180000, "later")
	frames := []framePart{
		{timestampMs: 30_000, ocrText: "slide A", frameName: "u1"},
		{timestampMs: 61_500, ocrText: " ", frameName: "u2"},
	}
	segs, err := merge([]model.TranscriptSegment{ts, ts2}, frames)
	if err != nil || len(segs) != 3 {
		t.Fatalf("%v %v", segs, err)
	}
	if segs[0].StartMs != 0 || segs[0].EndMs != 60000 || segs[0].Transcript != "hello" ||
		len(segs[0].OcrTexts) != 1 || segs[0].EvidenceFrames[0] != "u1" {
		t.Fatalf("seg0 %+v", segs[0])
	}
	if segs[1].StartMs != 60000 || segs[1].Transcript != "" || len(segs[1].OcrTexts) != 0 || segs[1].EvidenceFrames[0] != "u2" {
		t.Fatalf("seg1 %+v", segs[1])
	}
	if segs[2].StartMs != 120000 {
		t.Fatal("ordering")
	}
}

func TestParseShowinfoLine(t *testing.T) {
	tests := []struct {
		line string
		want int64
		ok   bool
	}{
		{"[Parsed_showinfo_1 @ 0x1] n:   0 pts:      0 pts_time:0       pos: 48 fmt:yuv420p", 0, true},
		{"[Parsed_showinfo_1 @ 0x1] n:   3 pts: 90000 pts_time:30.033 pos: 1", 30033, true},
		{"[Parsed_showinfo_1 @ 0x1] no timestamp here", 0, false},
		{"frame= 10 pts_time:5.0", 0, false}, // not a showinfo line
	}
	for _, tt := range tests {
		got, ok := ParseShowinfoLine(tt.line)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%q => %d,%v", tt.line, got, ok)
		}
	}
}

func TestCommandLines(t *testing.T) {
	a := FFmpegFrameArgs("in.mp4", "/tmp/f", 20)
	if FrameGapSeconds(600) != 20 || FrameGapSeconds(3600) != 91 || FrameGapSeconds(0) != 20 {
		t.Fatal("frame gap: 20 s for short videos, longer so that a long video keeps at most 40 frames")
	}
	if a[1] != "-skip_frame" || a[2] != "nokey" || a[5] != "-vf" || !strings.HasPrefix(a[6], "select=eq(n\\,0)") ||
		a[7] != "-vsync" || a[8] != "vfr" || a[9] != "/tmp/f/frame_%06d.jpg" {
		t.Fatalf("%q", a)
	}
	s := FFmpegSegmentArgs("in.mp4", "/tmp/a/audio_%03d.mp3")
	want := []string{"-y", "-i", "in.mp4", "-vn", "-ac", "1", "-ar", "16000", "-acodec", "libmp3lame", "-b:a", "48k", "-f", "segment", "-segment_time", "60", "-reset_timestamps", "1", "/tmp/a/audio_%03d.mp3"}
	for i := range want {
		if s[i] != want[i] {
			t.Fatalf("%q", s)
		}
	}
	o := OCRArgs("/x.jpg")
	if o[1] != "stdout" || o[2] != "-l" || o[3] != "chi_sim+eng" {
		t.Fatalf("%q", o)
	}
}

func writeJPEG(t *testing.T, path string, f func(x, y int) uint8) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 90, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 90; x++ {
			img.SetGray(x, y, color.Gray{Y: f(x, y)})
		}
	}
	fh, _ := os.Create(path)
	defer fh.Close()
	if err := jpeg.Encode(fh, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
}

func TestDifferenceHash(t *testing.T) {
	dir := t.TempDir()
	grad := filepath.Join(dir, "grad.jpg")
	rev := filepath.Join(dir, "rev.jpg")
	flat := filepath.Join(dir, "flat.jpg")
	writeJPEG(t, grad, func(x, _ int) uint8 { return uint8(255 - x*2) }) // brighter left => all bits set
	writeJPEG(t, rev, func(x, _ int) uint8 { return uint8(x * 2) })      // brighter right => no bits
	writeJPEG(t, flat, func(_, _ int) uint8 { return 128 })
	hg, hr, hf := DifferenceHash(grad), DifferenceHash(rev), DifferenceHash(flat)
	if hg != ^uint64(0) || hr != 0 || HammingDistance(hg, hr) != 64 {
		t.Fatalf("grad=%x rev=%x", hg, hr)
	}
	if HammingDistance(hf, hf) != 0 {
		t.Fatal("identical images")
	}
	if DifferenceHash(filepath.Join(dir, "missing.jpg")) != 0 {
		t.Fatal("unreadable => 0")
	}
}

type fakeASR struct{ err error }

func (f fakeASR) AudioToText(context.Context, string) (string, error) { return "", f.err }

func TestPermanentFailureSurvivesWrapping(t *testing.T) {
	// A 4xx ASR rejection must stay detectable as permanent after the layers of
	// internal-error wrapping (segments -> branches -> context build).
	rejected := common.InvalidArgument("ASR request rejected with HTTP 401")
	all := common.Internal("所有 ASR 分片均处理失败", rejected)
	both := common.Internal("ASR 和 OCR 分支均失败", all)
	built := common.Internal("VideoContext 构建失败", both)
	if !common.IsPermanentFailure(built) {
		t.Fatal("permanent cause lost")
	}
	transient := common.Internal("VideoContext 构建失败", common.Internal("ASR 调用失败，已达到最大重试次数", nil))
	if common.IsPermanentFailure(transient) {
		t.Fatal("transient failure misclassified")
	}
	_ = fakeASR{}
	_ = workerpool.ErrRejected
}

func TestOCRKeepsTesseractLogsOutOfTheText(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "tesseract")
	script := "#!/bin/sh\necho 'Estimating resolution as 132' >&2\necho '二叉树 Binary Tree'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(dir, "f.jpg")
	_ = os.WriteFile(img, []byte("jpg"), 0o644)
	got, err := NewOCR(fake).Recognize(context.Background(), img)
	if err != nil || got != "二叉树 Binary Tree" {
		t.Fatalf("got %q, %v", got, err)
	}
}
