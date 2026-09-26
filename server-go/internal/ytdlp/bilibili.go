package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"dovideo/server/internal/common"
)

// Bilibili answers yt-dlp from data-centre addresses with HTTP 412 on the video page, but its public player API
// still works without a login: pagelist gives a part's cid, and playurl with platform=html5 returns one H.264 + AAC
// mp4 (up to 720p) that can be downloaded with a browser User-Agent and a bilibili Referer.

const (
	biliAPI      = "https://api.bilibili.com"
	biliBrowser  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
	biliAPIAgent = "Vparser/1.0 (+https://vparser.yunfanteo.world)" // the API refuses browser agents without cookies
	maxDownload  = 2 << 30                                          // same 2 GB limit as yt-dlp's --max-filesize
)

var bvRe = regexp.MustCompile(`BV[0-9A-Za-z]{10}`)

// IsBilibili reports whether a host is a Bilibili video site or its b23.tv short links.
func IsBilibili(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "b23.tv" || h == "bilibili.com" || strings.HasSuffix(h, ".bilibili.com")
}

// bilibiliCDN reports whether a media URL points at Bilibili's own CDN (never anywhere the API could be tricked into).
func bilibiliCDN(host string) bool {
	h := strings.ToLower(host)
	for _, s := range []string{".bilivideo.com", ".bilivideo.cn", ".akamaized.net", ".hdslb.com"} {
		if strings.HasSuffix(h, s) {
			return true
		}
	}
	return false
}

// BilibiliTarget extracts the BV id and part number (?p=, default 1) from a Bilibili video URL.
func BilibiliTarget(u *url.URL) (string, int, bool) {
	bv := bvRe.FindString(u.Path)
	if bv == "" {
		return "", 0, false
	}
	p, err := strconv.Atoi(u.Query().Get("p"))
	if err != nil || p < 1 {
		p = 1
	}
	return bv, p, true
}

func (d *Downloader) biliClient() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (d *Downloader) biliAPIBase() string {
	if d.BiliAPI != "" {
		return d.BiliAPI
	}
	return biliAPI
}

// resolveShortLink turns https://b23.tv/xxxx into the video page it redirects to (one hop, Bilibili hosts only).
func (d *Downloader) resolveShortLink(ctx context.Context, u *url.URL) (*url.URL, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", biliBrowser)
	resp, err := d.biliClient().Do(req)
	if err != nil {
		return nil, common.Business(common.CodeSourceTimeout, "连接 B 站超时，请稍后重试或改为上传文件")
	}
	resp.Body.Close()
	next, err := u.Parse(resp.Header.Get("Location"))
	if err != nil || !IsBilibili(next.Hostname()) || strings.EqualFold(next.Hostname(), "b23.tv") {
		return nil, common.Business(common.CodeSourceUnsupported, "这个 B 站短链没有指向视频页，请复制视频页的链接")
	}
	return next, nil
}

type biliEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (d *Downloader) biliGet(ctx context.Context, path string, q url.Values, out any) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.biliAPIBase()+path+"?"+q.Encode(), nil)
	req.Header.Set("User-Agent", biliAPIAgent)
	resp, err := d.biliClient().Do(req)
	if err != nil {
		return common.Business(common.CodeSourceTimeout, "连接 B 站超时，请稍后重试或改为上传文件")
	}
	defer resp.Body.Close()
	var env biliEnvelope
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&env) != nil {
		return common.Business(common.CodeSourceBlocked, fmt.Sprintf("B 站拒绝了服务器的请求（HTTP %d），请先把视频保存到本地再上传", resp.StatusCode))
	}
	if env.Code != 0 {
		// -404: deleted or private; -403 / 87008 etc.: members only, paid or region locked
		msg := "B 站没有返回这个视频（可能已删除、仅会员可看或有地区限制）"
		return common.Business(common.CodeSourceBlocked, msg+"："+strings.TrimSpace(env.Message))
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// downloadBilibili fetches one part of a Bilibili video into a temp .mp4 and returns its path and a readable name.
func (d *Downloader) downloadBilibili(ctx context.Context, u *url.URL) (string, string, error) {
	if strings.EqualFold(u.Hostname(), "b23.tv") {
		next, err := d.resolveShortLink(ctx, u)
		if err != nil {
			return "", "", err
		}
		u = next
	}
	bv, part, ok := BilibiliTarget(u)
	if !ok {
		return "", "", common.Business(common.CodeSourceUnsupported, "只支持 B 站视频页的链接（地址里带 BV 号）")
	}

	var pages []struct {
		CID  int64  `json:"cid"`
		Page int    `json:"page"`
		Part string `json:"part"`
	}
	if err := d.biliGet(ctx, "/x/player/pagelist", url.Values{"bvid": {bv}}, &pages); err != nil {
		return "", "", err
	}
	if part > len(pages) {
		part = 1
	}
	if len(pages) == 0 {
		return "", "", common.Business(common.CodeSourceUnsupported, "这个 B 站视频没有可播放的分 P")
	}
	pg := pages[part-1]

	name := bv
	var view struct {
		Title string `json:"title"`
	}
	if d.biliGet(ctx, "/x/web-interface/view", url.Values{"bvid": {bv}}, &view) == nil && strings.TrimSpace(view.Title) != "" {
		name = view.Title
	}
	if len(pages) > 1 {
		name += fmt.Sprintf(" P%d %s", pg.Page, pg.Part)
	}

	var play struct {
		DURL []struct {
			URL  string `json:"url"`
			Size int64  `json:"size"`
		} `json:"durl"`
	}
	q := url.Values{"bvid": {bv}, "cid": {strconv.FormatInt(pg.CID, 10)}, "qn": {"64"}, "fnval": {"1"},
		"platform": {"html5"}, "high_quality": {"1"}}
	if err := d.biliGet(ctx, "/x/player/playurl", q, &play); err != nil {
		return "", "", err
	}
	if len(play.DURL) != 1 {
		return "", "", common.Business(common.CodeSourceUnsupported, "B 站没有给出可直接下载的视频文件，请先保存到本地再上传")
	}
	if play.DURL[0].Size > maxDownload {
		return "", "", common.Business(common.CodeSourceTooLarge, "视频超过 2 GB，无法通过链接导入")
	}
	media, err := url.Parse(play.DURL[0].URL)
	allowed := d.CDNAllowed
	if allowed == nil {
		allowed = bilibiliCDN
	}
	if err != nil || !allowed(media.Hostname()) {
		return "", "", common.Business(common.CodeSourceUnsupported, "B 站返回的下载地址不在其视频 CDN 上，已拒绝")
	}

	var path string
	if size := play.DURL[0].Size; size > chunkSize {
		path, err = d.fetchChunks(ctx, media.String(), size)
	} else {
		path, err = d.fetchFile(ctx, media.String(), size)
	}
	if err != nil {
		return "", "", err
	}
	slog.Info("url_video_downloaded", "host", "bilibili", "bvid", bv, "part", pg.Page)
	return path, name, nil
}

// fetchFile streams a Bilibili CDN file to a temp .mp4, stopping at the 2 GB limit. The CDN tends to drop a
// connection after about a minute or answer 5xx for a moment, so a dropped or short transfer resumes with a Range
// request from where it stopped. size is the length the API announced (0 if unknown).
func (d *Downloader) fetchFile(ctx context.Context, src string, size int64) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	out := filepath.Join(os.TempDir(), uuid.NewString()+".mp4")
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	fail := func(e error) (string, error) { f.Close(); _ = os.Remove(out); return "", e }

	var n int64
	mirrors, mirror := BilibiliMirrors(src), 0
	stalls := 0           // reconnects in a row that brought no new bytes; long videos need many reconnects that do
	next := func() bool { // move on to the next mirror, keeping the bytes already written
		if mirror+1 >= len(mirrors) {
			return false
		}
		mirror, stalls = mirror+1, 0
		slog.Info("bilibili_download_mirror", "host", hostOf(mirrors[mirror]), "bytes", n)
		return true
	}
	for attempt := 0; ; attempt++ {
		before := n
		req, _ := http.NewRequestWithContext(cctx, http.MethodGet, mirrors[mirror], nil)
		req.Header.Set("User-Agent", biliBrowser)
		req.Header.Set("Referer", "https://www.bilibili.com/")
		if n > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", n))
		}
		resp, err := d.biliClient().Do(req)
		if err == nil && (resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests) {
			resp.Body.Close() // a CDN hiccup: wait a little and try again
			err = fmt.Errorf("cdn answered HTTP %d", resp.StatusCode)
			resp = nil
		}
		if err == nil && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			if next() {
				continue
			}
			return fail(common.Business(common.CodeSourceBlocked, fmt.Sprintf("B 站视频服务器拒绝了下载（HTTP %d），请先把视频保存到本地再上传", resp.StatusCode)))
		}
		if err == nil && n > 0 && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close() // the server ignored Range: continuing would corrupt the file
			if next() {
				continue
			}
			return fail(common.Business(common.CodeSourceTimeout, "从 B 站下载视频时连接中断，请稍后重试"))
		}
		if err == nil {
			var m int64
			m, err = io.Copy(f, io.LimitReader(resp.Body, maxDownload+1-n))
			resp.Body.Close()
			n += m
			if n > maxDownload {
				return fail(common.Business(common.CodeSourceTooLarge, "视频超过 2 GB，无法通过链接导入"))
			}
			if err == nil && size > 0 && n < size {
				err = io.ErrUnexpectedEOF // closed cleanly but short
			}
		}
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return fail(ctx.Err()) // the caller went away (closed the page): not a timeout
		}
		if cctx.Err() != nil {
			return fail(common.Internal("视频链接下载超时", err))
		}
		if n > before {
			stalls = 0
		} else {
			stalls++
		}
		if stalls >= 4 && next() {
			continue
		}
		if stalls >= 5 {
			return fail(common.Business(common.CodeSourceTimeout, "从 B 站下载视频时连接反复中断，请稍后重试或改为上传文件"))
		}
		slog.Info("bilibili_download_resume", "bytes", n, "attempt", attempt+1, "err", err.Error())
		select {
		case <-cctx.Done():
			return fail(common.Internal("视频链接下载超时", cctx.Err()))
		case <-time.After(time.Duration(stalls+1) * time.Second):
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(out)
		return "", err
	}
	if n == 0 {
		_ = os.Remove(out)
		return "", common.Business(common.CodeSourceUnsupported, "B 站返回了空文件，请先保存到本地再上传")
	}
	return out, nil
}

// Bilibili serves the same object under the same path and signed query from several CDNs. The html5 playurl hands
// out an Akamai mirror, which from Singapore truncates some ranges and answers 503 for others; Bilibili's overseas
// COS mirror delivered a 26 MB file in 0.3 s in the same test. So downloads go to that mirror first and fall back to
// the original host and two domestic mirrors.
var biliMirrorHosts = []string{"upos-sz-mirrorcosov.bilivideo.com", "", "upos-sz-mirrorcos.bilivideo.com", "upos-sz-mirrorali.bilivideo.com"}

// BilibiliMirrors lists the URLs to try for a Bilibili media URL, best first ("" stands for the original host).
func BilibiliMirrors(src string) []string {
	u, err := url.Parse(src)
	if err != nil || !strings.HasPrefix(u.Path, "/upgcxcode/") || !bilibiliCDN(u.Hostname()) {
		return []string{src}
	}
	out, seen := []string{}, map[string]bool{}
	for _, h := range biliMirrorHosts {
		v := *u
		if h != "" {
			v.Host = h
		}
		if s := v.String(); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// A single connection to Bilibili's CDN runs at 0.2–1 MB/s from Singapore for videos that are not cached at the
// edge, so a 1-hour lecture took over half an hour. Large files are therefore fetched as 4 MB ranges, eight at a
// time, each range retried on its own and moved to the next mirror when one keeps failing.
const (
	chunkSize    = 4 << 20
	chunkWorkers = 8
)

func (d *Downloader) fetchChunks(ctx context.Context, src string, size int64) (string, error) {
	if size > maxDownload {
		return "", common.Business(common.CodeSourceTooLarge, "视频超过 2 GB，无法通过链接导入")
	}
	cctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	out := filepath.Join(os.TempDir(), uuid.NewString()+".mp4")
	f, err := os.Create(out)
	if err != nil {
		return "", err
	}
	fail := func(e error) (string, error) { f.Close(); _ = os.Remove(out); return "", e }
	if err := f.Truncate(size); err != nil {
		return fail(err)
	}
	mirrors := BilibiliMirrors(src)
	jobs := make(chan int64)
	errs := make(chan error, chunkWorkers)
	var wg sync.WaitGroup
	for w := 0; w < chunkWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for start := range jobs {
				end := min(start+chunkSize, size) - 1
				if err := d.fetchRange(cctx, mirrors, f, start, end); err != nil {
					errs <- err
					cancel() // one range gave up everywhere: stop the others
					return
				}
			}
		}()
	}
feed:
	for start := int64(0); start < size; start += chunkSize {
		select {
		case jobs <- start:
		case <-cctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)
	if err := <-errs; err != nil {
		return fail(err)
	}
	if ctx.Err() != nil {
		return fail(ctx.Err())
	}
	if cctx.Err() != nil {
		return fail(common.Internal("视频链接下载超时", cctx.Err()))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(out)
		return "", err
	}
	return out, nil
}

// fetchRange writes bytes [start, end] of the file at their offset, resuming inside the range after a drop and
// moving through the mirrors when one makes no progress several times in a row.
func (d *Downloader) fetchRange(ctx context.Context, mirrors []string, f *os.File, start, end int64) error {
	pos, mirror, stalls := start, 0, 0
	for pos <= end {
		before := pos
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, mirrors[mirror], nil)
		req.Header.Set("User-Agent", biliBrowser)
		req.Header.Set("Referer", "https://www.bilibili.com/")
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", pos, end))
		resp, err := d.biliClient().Do(req)
		if err == nil {
			if resp.StatusCode == http.StatusPartialContent {
				buf := make([]byte, 64<<10)
				for pos <= end {
					k, rerr := resp.Body.Read(buf[:min(int64(len(buf)), end-pos+1)])
					if k > 0 {
						if _, werr := f.WriteAt(buf[:k], pos); werr != nil {
							resp.Body.Close()
							return werr
						}
						pos += int64(k)
					}
					if rerr != nil {
						break
					}
				}
			}
			resp.Body.Close()
		}
		if pos > end {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if pos > before {
			stalls = 0
			continue
		}
		stalls++
		if stalls >= 3 {
			if mirror+1 >= len(mirrors) {
				return common.Business(common.CodeSourceTimeout, "从 B 站下载视频时连接反复中断，请稍后重试或改为上传文件")
			}
			mirror, stalls = mirror+1, 0
			slog.Info("bilibili_range_mirror", "host", hostOf(mirrors[mirror]), "offset", pos)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(stalls) * time.Second):
		}
	}
	return nil
}
