package ytdlp

import (
	"context"
	"net"
	"testing"
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
