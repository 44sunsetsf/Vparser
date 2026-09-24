package upload

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/redislock"
)

func TestValidateChallengeRequest(t *testing.T) {
	good := strings.Repeat("A", 32)
	tests := []struct {
		name string
		req  ChallengeRequest
		ok   bool
	}{
		{"valid, md5 lower-cased", ChallengeRequest{good, 10, "dir/a.mp4"}, true},
		{"short md5", ChallengeRequest{"abc", 10, "a.mp4"}, false},
		{"non-hex md5", ChallengeRequest{strings.Repeat("z", 32), 10, "a.mp4"}, false},
		{"zero size", ChallengeRequest{good, 0, "a.mp4"}, false},
		{"not a video", ChallengeRequest{good, 10, "a.exe"}, false},
	}
	for _, tt := range tests {
		got, err := ValidateChallengeRequest(tt.req)
		if (err == nil) != tt.ok {
			t.Fatalf("%s: err=%v", tt.name, err)
		}
		if tt.ok && (got.MD5 != strings.ToLower(good) || got.Filename != "a.mp4") {
			t.Fatalf("%s: normalised %+v", tt.name, got)
		}
	}
}

func TestChallengeRange(t *testing.T) {
	fixed := func(v int64) func(int64) (int64, error) {
		return func(n int64) (int64, error) {
			if v >= n {
				return 0, errors.New("out of range")
			}
			return v, nil
		}
	}
	tests := []struct {
		name         string
		size, pick   int64
		off, length  int64
		expectErrNil bool
	}{
		{"small file: whole file, offset 0", 100, 0, 0, 100, true},
		{"exactly 1MiB", 1 << 20, 0, 0, 1 << 20, true},
		{"large file: 1MiB at random offset", 10 << 20, 12345, 12345, 1 << 20, true},
		{"last valid offset", (1 << 20) + 5, 5, 5, 1 << 20, true},
	}
	for _, tt := range tests {
		off, length, err := ChallengeRange(tt.size, fixed(tt.pick))
		if (err == nil) != tt.expectErrNil || off != tt.off || length != tt.length || off+length > tt.size {
			t.Fatalf("%s: off=%d len=%d err=%v", tt.name, off, length, err)
		}
	}
}

func TestVerifyRange(t *testing.T) {
	sum := md5.Sum([]byte("hello"))
	h := hex.EncodeToString(sum[:])
	tests := []struct {
		data  string
		claim string
		want  bool
	}{
		{"hello", h, true},
		{"hello", strings.ToUpper(h), true},
		{"hellO", h, false},
		{"hello", "not-hex", false},
		{"hello", h[:30], false},
	}
	for _, tt := range tests {
		if got := VerifyRange([]byte(tt.data), tt.claim); got != tt.want {
			t.Errorf("%q/%q: %v", tt.data, tt.claim, got)
		}
	}
}

type fakeStore struct{ objects map[string][]byte }

func (f *fakeStore) IsManagedFile(u string) bool { return strings.HasPrefix(u, "http://minio/") }
func (f *fakeStore) ObjectSize(_ context.Context, u string) (int64, error) {
	b, ok := f.objects[u]
	if !ok {
		return 0, errors.New("missing")
	}
	return int64(len(b)), nil
}
func (f *fakeStore) ReadRange(_ context.Context, u string, off, n int64) ([]byte, error) {
	return f.objects[u][off : off+n], nil
}

type fakeCopies struct {
	rows  []model.MediaFile
	saved []string
}

func (f *fakeCopies) FindByContentHash(_ context.Context, h string) ([]model.MediaFile, error) {
	var out []model.MediaFile
	for _, r := range f.rows {
		if r.ContentHash != nil && *r.ContentHash == h {
			out = append(out, r)
		}
	}
	return out, nil
}
func (f *fakeCopies) SaveReference(_ context.Context, name, path string, uid int64, h string) (*model.MediaFile, error) {
	f.saved = append(f.saved, path)
	return &model.MediaFile{ID: 99, Filename: name, FilePath: path, UserID: uid, ContentHash: &h}, nil
}

func TestInstantUploadFlow(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	content := []byte(strings.Repeat("0123456789", 300_000)) // 3MB
	sum := md5.Sum(content)
	hash := hex.EncodeToString(sum[:])
	store := &fakeStore{objects: map[string][]byte{"http://minio/media/a.mp4": content}}
	copies := &fakeCopies{rows: []model.MediaFile{{ID: 1, FilePath: "http://minio/media/a.mp4", ContentHash: &hash}}}
	s := NewInstant(rdb, redislock.New(rdb), store, copies)
	s.randInt = func(int64) (int64, error) { return 777, nil }
	ctx := context.Background()

	// unknown hash or different size: no challenge, fall back to chunked upload
	if ch, err := s.Challenge(ctx, 7, ChallengeRequest{strings.Repeat("0", 32), 10, "b.mp4"}); ch != nil || err != nil {
		t.Fatalf("unknown hash: %v %v", ch, err)
	}
	if ch, err := s.Challenge(ctx, 7, ChallengeRequest{hash, int64(len(content)) - 1, "b.mp4"}); ch != nil || err != nil {
		t.Fatalf("size mismatch: %v %v", ch, err)
	}

	ch, err := s.Challenge(ctx, 7, ChallengeRequest{hash, int64(len(content)), "b.mp4"})
	if err != nil || ch == nil || ch.Offset != 777 || ch.Length != 1<<20 {
		t.Fatalf("challenge %+v %v", ch, err)
	}
	if ttl := mr.TTL(challengeKeyPrefix + ch.ChallengeID); ttl != 2*time.Minute {
		t.Fatalf("challenge ttl %v", ttl)
	}
	rangeSum := md5.Sum(content[ch.Offset : ch.Offset+ch.Length])
	good := hex.EncodeToString(rangeSum[:])

	// another user cannot redeem it, and the attempt burns it
	if _, err := s.Prove(ctx, 8, ProofRequest{ch.ChallengeID, good}); !common.Has(err, common.KindForbidden) {
		t.Fatalf("foreign user: %v", err)
	}
	if _, err := s.Prove(ctx, 7, ProofRequest{ch.ChallengeID, good}); !common.Has(err, common.KindInvalidArgument) {
		t.Fatalf("challenge must be single-use: %v", err)
	}

	// a wrong answer is rejected and also consumes the challenge
	ch, _ = s.Challenge(ctx, 7, ChallengeRequest{hash, int64(len(content)), "b.mp4"})
	if _, err := s.Prove(ctx, 7, ProofRequest{ch.ChallengeID, hash}); !common.Has(err, common.KindInvalidArgument) {
		t.Fatalf("whole-file md5 is not a valid range answer: %v", err)
	}

	// correct answer: a new row referencing the same object
	ch, _ = s.Challenge(ctx, 7, ChallengeRequest{hash, int64(len(content)), "b.mp4"})
	mf, err := s.Prove(ctx, 7, ProofRequest{ch.ChallengeID, good})
	if err != nil || mf.UserID != 7 || mf.FilePath != "http://minio/media/a.mp4" || len(copies.saved) != 1 {
		t.Fatalf("prove %+v %v", mf, err)
	}
	if mr.Exists(challengeKeyPrefix+ch.ChallengeID) || mr.Exists("lock:media-object:"+hash) {
		t.Fatal("challenge and object lock must be released")
	}

	// expired challenge
	ch, _ = s.Challenge(ctx, 7, ChallengeRequest{hash, int64(len(content)), "b.mp4"})
	mr.FastForward(3 * time.Minute)
	if _, err := s.Prove(ctx, 7, ProofRequest{ch.ChallengeID, good}); !common.Has(err, common.KindInvalidArgument) {
		t.Fatalf("expired: %v", err)
	}
}
