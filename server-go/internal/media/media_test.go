package media

import "testing"

func TestNormalizeVideoFilename(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"a.mp4", "a.mp4", false},
		{"C:\\videos\\clip.MOV", "clip.MOV", false},
		{"/x/y/z.webm", "z.webm", false},
		{"  spaced.mkv ", "spaced.mkv", false},
		{"", "", true},
		{"   ", "", true},
		{"noext", "", true},
		{"a.txt", "", true},
		{"dir/", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeVideoFilename(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("%q => %q, %v", tt.in, got, err)
		}
	}
	long := make([]byte, 252)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := NormalizeVideoFilename(string(long) + ".mp4"); err == nil {
		t.Fatal("length > 255 must fail")
	}
}
