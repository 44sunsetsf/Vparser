package ytdlp

import (
	"context"
	"net"
	"strings"
	"testing"

	"dovideo/server/internal/common"
)

func TestIsDisallowedAddress(t *testing.T) {
	tests := map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "172.16.0.1": true, "192.168.1.1": true, "169.254.169.254": true,
		"0.0.0.0": true, "100.64.0.1": true, "100.127.255.255": true, "100.128.0.1": false, "240.0.0.1": true,
		"224.0.0.1": true, "8.8.8.8": false, "::1": true, "fe80::1": true, "fc00::1": true, "fd12::1": true,
		"2606:4700::1111": false, "::ffff:127.0.0.1": true,
	}
	for ip, want := range tests {
		if got := IsDisallowedAddress(net.ParseIP(ip)); got != want {
			t.Errorf("%s: got %v want %v", ip, got, want)
		}
	}
}

func TestValidateURL(t *testing.T) {
	d := New("yt-dlp", "")
	d.Resolver = func(_ context.Context, host string) ([]net.IP, error) {
		if host == "internal.example" {
			return []net.IP{net.ParseIP("10.0.0.5")}, nil
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	for _, bad := range []string{"ftp://a.com/x", "file:///etc/passwd", "http:///x", "http://internal.example/v"} {
		if err := d.ValidatePublicHTTPURL(context.Background(), bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if err := d.ValidatePublicHTTPURL(context.Background(), "https://example.com/v.mp4"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildArgs(t *testing.T) {
	d := New("yt-dlp", "/opt/ff")
	a := d.BuildArgs("/tmp/o.mp4", "https://x/y")
	if a[0] != "--no-playlist" || a[len(a)-1] != "https://x/y" || a[len(a)-3] != "-o" {
		t.Fatalf("args: %v", a)
	}
	found := false
	for i, s := range a {
		if s == "--ffmpeg-location" && a[i+1] == "/opt/ff" {
			found = true
		}
	}
	if !found {
		t.Fatal("ffmpeg-location missing")
	}
}

func TestBuildArgsFallsBackToAnyFormat(t *testing.T) {
	a := New("yt-dlp", "").BuildArgs("/tmp/o.mp4", "https://x/y.mp4")
	for i, s := range a {
		if s == "-f" && strings.HasSuffix(a[i+1], "/b[ext=mp4]/bv*+ba/b") {
			return
		}
	}
	t.Fatalf("format selector has no fallback: %v", a)
}

func TestClassify(t *testing.T) {
	cases := []struct {
		log  string
		code common.ErrorCode
	}{
		{"ERROR: [youtube] x: Sign in to confirm you’re not a bot. Use --cookies-from-browser", common.CodeSourceBlocked},
		{"ERROR: [BiliBili] 1GJ411x7h7: Unable to download webpage: HTTP Error 412: Precondition Failed", common.CodeSourceBlocked},
		{"ERROR: [Douyin] 73: Fresh cookies (not necessarily logged in) are needed", common.CodeSourceBlocked},
		{"ERROR: [vimeo] 7: The web client only works when logged-in.", common.CodeSourceBlocked},
		{"ERROR: Unsupported URL: https://example.com/page", common.CodeSourceUnsupported},
		{"ERROR: [generic] y: Requested format is not available", common.CodeSourceUnsupported},
		{"ERROR: File is larger than max-filesize (3000000000 bytes > 2147483648 bytes). Aborting.", common.CodeSourceTooLarge},
		{"ERROR: [archive.org] x: Connection to archive.org timed out. (connect timeout=20.0)", common.CodeSourceTimeout},
	}
	for _, c := range cases {
		e := Classify(c.log)
		if e == nil || e.Code != c.code {
			t.Errorf("%q → %+v, want code %d", c.log, e, c.code.Code)
		}
	}
	if Classify("ERROR: something nobody has seen before") != nil {
		t.Error("unknown failures should stay internal")
	}
}
