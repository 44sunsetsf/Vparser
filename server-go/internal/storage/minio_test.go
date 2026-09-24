package storage

import "testing"

func TestFileSuffix(t *testing.T) {
	tests := map[string]string{
		"a.MP4": ".mp4", "noext": "", "x.verylongextension": "", "a.b c": "", "video.mov": ".mov", "": "",
	}
	for in, want := range tests {
		if got := fileSuffix(in); got != want {
			t.Errorf("fileSuffix(%q)=%q want %q", in, got, want)
		}
	}
}

func TestObjectNameAndManaged(t *testing.T) {
	m := &Minio{bucket: "media", endpoint: "http://localhost:9000"}
	if !m.IsManagedFile("http://localhost:9000/media/a.mp4") || m.IsManagedFile("http://other/media/a.mp4") {
		t.Fatal("managed check")
	}
	if n, err := m.objectName("http://localhost:9000/media/evidence-frames/x.jpg"); err != nil || n != "evidence-frames/x.jpg" {
		t.Fatalf("objectName=%q %v", n, err)
	}
	if !m.IsManagedFileWithPrefix("http://localhost:9000/media/evidence-frames/x.jpg", "evidence-frames") ||
		m.IsManagedFileWithPrefix("http://localhost:9000/media/x.jpg", "evidence-frames") {
		t.Fatal("prefix check")
	}
	if _, err := m.objectName("http://localhost:9000/media/../etc"); err == nil {
		t.Fatal("path traversal must be rejected")
	}
}
