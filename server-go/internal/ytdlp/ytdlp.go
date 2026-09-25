// Package ytdlp validates user-supplied video URLs (SSRF guard) and downloads them with yt-dlp.
package ytdlp

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"dovideo/server/internal/common"
)

const downloadTimeout = 30 * time.Minute

// Downloader runs yt-dlp.
type Downloader struct {
	Path      string
	FfmpegDir string
	// Resolver is overridable in tests.
	Resolver func(ctx context.Context, host string) ([]net.IP, error)
	// For the Bilibili path (bilibili.go); all overridable in tests.
	HTTP       *http.Client
	BiliAPI    string
	CDNAllowed func(host string) bool
}

func New(path, ffmpegDir string) *Downloader {
	return &Downloader{Path: path, FfmpegDir: ffmpegDir, Resolver: func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}}
}

// BuildArgs returns the yt-dlp argument list.
func (d *Downloader) BuildArgs(output, rawURL string) []string {
	args := []string{
		"--no-playlist", "--socket-timeout", "30", "--retries", "3", "--max-filesize", "2048M",
		// H.264 + AAC first (no re-encoding needed); then any mp4; then whatever the site has, remuxed to mp4.
		// A plain file link has no codec metadata, so the H.264-only selectors alone would reject it.
		"-f", "bv*[vcodec^=avc1][ext=mp4]+ba[acodec^=mp4a][ext=m4a]/b[vcodec^=avc1][ext=mp4]/bv*[vcodec^=avc1]+ba[acodec^=mp4a]/b[ext=mp4]/bv*+ba/b",
		"--merge-output-format", "mp4", "--recode-video", "mp4",
	}
	if strings.TrimSpace(d.FfmpegDir) != "" {
		args = append(args, "--ffmpeg-location", d.FfmpegDir)
	}
	return append(args, "-o", output, rawURL)
}

// Download fetches the video into a temp .mp4 and returns its path and, when the site provides one, its title.
func (d *Downloader) Download(ctx context.Context, rawURL string) (string, string, error) {
	if err := d.ValidatePublicHTTPURL(ctx, rawURL); err != nil {
		return "", "", err
	}
	if u, err := url.Parse(rawURL); err == nil && IsBilibili(u.Hostname()) {
		return d.downloadBilibili(ctx, u)
	}
	path, err := d.runYtDlp(ctx, rawURL)
	return path, "", err
}

func (d *Downloader) runYtDlp(ctx context.Context, rawURL string) (string, error) {
	output := filepath.Join(os.TempDir(), uuid.NewString()+".mp4")
	logFile, err := os.CreateTemp("", "yt-dlp-*.log")
	if err != nil {
		return "", err
	}
	defer os.Remove(logFile.Name())
	defer logFile.Close()

	cctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, d.Path, d.BuildArgs(output, rawURL)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	runErr := cmd.Run()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		_ = os.Remove(output)
		return "", common.Internal("视频链接下载超时", nil)
	}
	st, statErr := os.Stat(output)
	if runErr != nil || statErr != nil || !st.Mode().IsRegular() {
		logs, _ := os.ReadFile(logFile.Name())
		_ = os.Remove(output)
		if known := Classify(string(logs)); known != nil {
			slog.Warn("url_video_refused", "reason", known.Msg, "log", tail(string(logs), 600))
			return "", known
		}
		return "", common.Internal("yt-dlp 下载失败: "+tail(string(logs), 2000), nil)
	}
	host := ""
	if u, err := url.Parse(rawURL); err == nil {
		host = u.Hostname()
	}
	slog.Info("url_video_downloaded", "host", host, "bytes", st.Size())
	return output, nil
}

// ValidatePublicHTTPURL rejects non-http(s) URLs and hosts resolving to local/private ranges.
func (d *Downloader) ValidatePublicHTTPURL(ctx context.Context, value string) error {
	u, err := url.Parse(value)
	if err != nil {
		return common.InvalidArgument("仅支持合法的公网 HTTP/HTTPS 视频链接")
	}
	host := u.Hostname()
	if host == "" || !(strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) {
		return common.InvalidArgument("仅支持合法的公网 HTTP/HTTPS 视频链接")
	}
	ips, err := d.Resolver(ctx, host)
	if err != nil {
		return err // DNS failure is an infrastructure error (500), not a bad URL (400)
	}
	if len(ips) == 0 {
		return common.InvalidArgument("无法解析视频链接的主机地址")
	}
	for _, ip := range ips {
		if IsDisallowedAddress(ip) {
			return common.InvalidArgument("不允许访问本机、内网或保留网段地址")
		}
	}
	return nil
}

// IsDisallowedAddress rejects loopback, private, link-local and other non-public addresses.
func IsDisallowedAddress(ip net.IP) bool {
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		first, second := v4[0], v4[1]
		if first == 0 {
			return true
		}
		if first == 100 && second >= 64 && second <= 127 {
			return true
		}
		if first == 169 && second == 254 {
			return true
		}
		return first >= 240
	}
	if len(ip) == 16 {
		return ip[0]&0xFE == 0xFC || ip[0] == 0xFE && ip[1]&0xC0 == 0xC0 // fc00::/7, deprecated fec0::/10
	}
	return false
}

// Classify turns the usual yt-dlp failures into errors the visitor can act on; nil means an unexpected failure.
// Video platforms (Bilibili, YouTube, Douyin, Vimeo …) now refuse data-centre addresses without a login,
// so the advice is always the same: save the video locally and upload the file.
func Classify(logs string) *common.Error {
	l := strings.ToLower(logs)
	has := func(parts ...string) bool {
		for _, p := range parts {
			if strings.Contains(l, p) {
				return true
			}
		}
		return false
	}
	switch {
	case has("larger than max-filesize", "file is larger than"):
		return common.Business(common.CodeSourceTooLarge, "视频超过 2 GB，无法通过链接导入")
	case has("sign in to confirm", "cookies", "logged-in", "login required", "log in", "http error 412", "http error 403",
		"private video", "not available in your country", "geo restricted", "geo-restricted"):
		return common.Business(common.CodeSourceBlocked, "该网站拒绝了服务器的下载请求，请先把视频保存到本地，再用上传功能导入")
	case has("unsupported url", "no video formats found", "requested format is not available", "unable to extract",
		"http error 404", "is not a valid url"):
		return common.Business(common.CodeSourceUnsupported, "这个链接里没有找到可下载的视频，请换成视频文件的直链（如 .mp4）")
	case has("timed out", "timeout"):
		return common.Business(common.CodeSourceTimeout, "连接视频网站超时，请稍后重试或改为上传文件")
	}
	return nil
}

func tail(v string, max int) string {
	if len(v) <= max {
		return v
	}
	return v[len(v)-max:]
}
