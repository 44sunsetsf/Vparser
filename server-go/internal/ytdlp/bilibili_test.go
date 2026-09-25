package ytdlp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"dovideo/server/internal/common"
)

func TestIsBilibiliAndTarget(t *testing.T) {
	for host, want := range map[string]bool{"www.bilibili.com": true, "m.bilibili.com": true, "b23.tv": true,
		"bilibili.com": true, "evilbilibili.com": false, "youtube.com": false} {
		if IsBilibili(host) != want {
			t.Errorf("IsBilibili(%q) != %v", host, want)
		}
	}
	u, _ := url.Parse("https://www.bilibili.com/video/BV1J6eQ6iEyN/?p=3&spm=x")
	if bv, p, ok := BilibiliTarget(u); !ok || bv != "BV1J6eQ6iEyN" || p != 3 {
		t.Fatalf("got %s %d %v", bv, p, ok)
	}
	u, _ = url.Parse("https://www.bilibili.com/bangumi/play/ep1")
	if _, _, ok := BilibiliTarget(u); ok {
		t.Fatal("a page without a BV id is not a video")
	}
}

func TestBilibiliCDNAllowList(t *testing.T) {
	for host, want := range map[string]bool{"upos-hz-mirrorakam.akamaized.net": true, "cn-gdfs-ct-01.bilivideo.com": true,
		"169.254.169.254": false, "evil.com": false} {
		if bilibiliCDN(host) != want {
			t.Errorf("bilibiliCDN(%q) != %v", host, want)
		}
	}
}

// fakeBilibili serves the three API calls and the media file the downloader makes.
func fakeBilibili(t *testing.T, playCode int) (*Downloader, *httptest.Server) {
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/x/player/pagelist", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":[{"cid":11,"page":1,"part":"intro"},{"cid":22,"page":2,"part":"demo"}]}`))
	})
	mux.HandleFunc("/x/web-interface/view", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"title":"Raft 共识算法"}}`))
	})
	mux.HandleFunc("/x/player/playurl", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("cid") != "22" || r.URL.Query().Get("platform") != "html5" {
			t.Errorf("playurl query: %s", r.URL.RawQuery)
		}
		if playCode != 0 {
			_, _ = w.Write([]byte(`{"code":-404,"message":"啥都木有"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"durl":[{"url":"` + srv.URL + `/media.mp4","size":4}]}}`))
	})
	drops, hiccups := 1, 1 // first transfer cut after two bytes, then one 504, like the real CDN
	mux.HandleFunc("/media.mp4", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://www.bilibili.com/" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if rg := r.Header.Get("Range"); rg != "" && hiccups > 0 {
			hiccups--
			w.WriteHeader(http.StatusGatewayTimeout)
			return
		}
		if rg := r.Header.Get("Range"); rg != "" {
			if rg != "bytes=2-" {
				t.Errorf("resume range %q", rg)
			}
			w.Header().Set("Content-Range", "bytes 2-3/4")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("4!"))
			return
		}
		w.Header().Set("Content-Length", "4")
		_, _ = w.Write([]byte("mp"))
		w.(http.Flusher).Flush()
		if drops > 0 {
			drops--
			panic(http.ErrAbortHandler) // drop the connection mid-body
		}
		_, _ = w.Write([]byte("4!"))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	d := New("yt-dlp", "")
	d.HTTP, d.BiliAPI, d.CDNAllowed = srv.Client(), srv.URL, func(string) bool { return true }
	return d, srv
}

func TestDownloadBilibiliPart(t *testing.T) {
	d, _ := fakeBilibili(t, 0)
	u, _ := url.Parse("https://www.bilibili.com/video/BV1J6eQ6iEyN?p=2")
	path, name, err := d.downloadBilibili(context.Background(), u) // the fake CDN drops the first transfer
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if b, _ := os.ReadFile(path); string(b) != "mp4!" {
		t.Fatalf("file content %q", b)
	}
	if name != "Raft 共识算法 P2 demo" {
		t.Fatalf("name %q", name)
	}
}

func TestDownloadBilibiliRefusedIsExplained(t *testing.T) {
	d, _ := fakeBilibili(t, -404)
	u, _ := url.Parse("https://www.bilibili.com/video/BV1J6eQ6iEyN?p=2")
	_, _, err := d.downloadBilibili(context.Background(), u)
	var e *common.Error
	if !errors.As(err, &e) || e.Code != common.CodeSourceBlocked {
		t.Fatalf("want a SourceBlocked business error, got %v", err)
	}
}

func TestDownloadBilibiliRejectsForeignCDN(t *testing.T) {
	d, _ := fakeBilibili(t, 0)
	d.CDNAllowed = nil // the real allow-list: the fake server's 127.0.0.1 is not Bilibili's CDN
	u, _ := url.Parse("https://www.bilibili.com/video/BV1J6eQ6iEyN?p=2")
	if _, _, err := d.downloadBilibili(context.Background(), u); err == nil {
		t.Fatal("a download URL outside Bilibili's CDN must be refused")
	}
}
