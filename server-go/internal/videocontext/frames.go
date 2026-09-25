package videocontext

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dovideo/server/internal/common"
)

var ptsTime = regexp.MustCompile(`pts_time:([0-9.]+)`)

// maxKeyFrames caps how many frames get OCR, whatever the video length: OCR is the slowest step on a small
// server (about 4 s a frame), so a 1-hour lecture must not turn into 170 slide changes.
const maxKeyFrames = 40

// FrameGapSeconds is the minimum distance between two kept frames: 20 s, or longer for long videos so that at most
// maxKeyFrames are kept.
func FrameGapSeconds(durationSec float64) int {
	return max(20, int(durationSec/maxKeyFrames)+1)
}

// FFmpegFrameArgs is the key frame command line. Only the codec's own key frames are decoded (-skip_frame nokey):
// decoding every frame for scene detection used to take most of the preprocessing time on a small server. A frame
// is kept on a scene change (a new slide) at most once per gap seconds, and at least once per max(60, gap) seconds;
// frames are scaled to at most 1280 px wide, which is plenty for OCR and much faster.
func FFmpegFrameArgs(video, frameDir string, gap int) []string {
	return []string{"-y", "-skip_frame", "nokey", "-i", video,
		"-vf", fmt.Sprintf(`select=eq(n\,0)+gte(t-prev_selected_t\,%d)+gt(scene\,0.3)*gte(t-prev_selected_t\,%d),scale='min(1280\,iw)':-2,showinfo`, max(60, gap), gap),
		"-vsync", "vfr",
		filepath.Join(frameDir, "frame_%06d.jpg")}
}

// probeDuration returns the video length in seconds (0 if ffprobe cannot tell).
func probeDuration(ctx context.Context, video string) float64 {
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := exec.CommandContext(cctx, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", video).Output()
	if err != nil {
		return 0
	}
	d, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return d
}

// runFrameCommand runs ffmpeg with merged stdout/stderr in a log file and returns pts_time of
// every showinfo line in milliseconds.
func runFrameCommand(ctx context.Context, args []string) ([]int64, error) {
	logFile, err := os.CreateTemp("", "dovideo-ffmpeg-*.log")
	if err != nil {
		return nil, err
	}
	defer os.Remove(logFile.Name())
	defer logFile.Close()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ffmpeg", args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	runErr := cmd.Run()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return nil, common.Internal("FFmpeg 执行超时", nil)
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, common.Internal("FFmpeg 执行失败", runErr)
	}
	return parseTimestamps(logFile.Name())
}

func parseTimestamps(logPath string) ([]int64, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []int64
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		if ts, ok := ParseShowinfoLine(sc.Text()); ok {
			out = append(out, ts)
		}
	}
	return out, sc.Err()
}

// ParseShowinfoLine extracts (long)(pts_time*1000) from a showinfo log line.
func ParseShowinfoLine(line string) (int64, bool) {
	if !strings.Contains(line, "showinfo") {
		return 0, false
	}
	m := ptsTime.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	d, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return int64(d * 1000), true
}

// DifferenceHash is a 9x8 gray dHash (bit set when a pixel is brighter than its right neighbour).
// Unreadable images hash to 0, so they collapse into one "unknown" bucket instead of failing.
func DifferenceHash(path string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return 0
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return 0
	}
	var gray [8][9]uint32
	for y := 0; y < 8; y++ {
		for x := 0; x < 9; x++ {
			sx := b.Min.X + (2*x+1)*w/18
			sy := b.Min.Y + (2*y+1)*h/16
			r, g, bl, _ := img.At(sx, sy).RGBA()
			gray[y][x] = (299*r + 587*g + 114*bl) / 1000
		}
	}
	var hash uint64
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			hash <<= 1
			if gray[y][x] > gray[y][x+1] {
				hash |= 1
			}
		}
	}
	return hash
}

// HammingDistance is Long.bitCount(a ^ b).
func HammingDistance(a, b uint64) int { return bits.OnesCount64(a ^ b) }
