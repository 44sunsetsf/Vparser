package media

import "testing"

func TestShouldDeleteObject(t *testing.T) {
	tests := []struct {
		name      string
		managed   bool
		locked    bool
		remaining int64
		want      bool
	}{
		{"last reference to a managed object", true, true, 0, true},
		{"another row still references it", true, true, 1, false},
		{"many references", true, true, 3, false},
		{"foreign URL is never deleted", false, true, 0, false},
		{"count not taken under the lock", true, false, 0, false},
	}
	for _, tt := range tests {
		if got := ShouldDeleteObject(tt.managed, tt.locked, tt.remaining); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
	if ObjectLockKey("abc") != "lock:media-object:abc" {
		t.Fatal("lock key")
	}
}
