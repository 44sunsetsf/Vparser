package auth

import (
	"context"
	"dovideo/server/internal/model"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// Reference vectors: PBKDF2-HMAC-SHA256, 256-bit key, salt = bytes 0..15, computed independently
// of this package so a regression in derive or the encoding cannot validate itself.
const (
	refHash210k = "pbkdf2$210000$AAECAwQFBgcICQoLDA0ODw$UnOIggFRGPeDyPNMVyR6R+Uf0xODtMm/ZbhrPFHEhZQ"
	refHash1k   = "pbkdf2$1000$AAECAwQFBgcICQoLDA0ODw$+QpjUt3gLJBO74Q87Je65YfxK4FVVPYDTz/6DTeuwLI"
)

func TestPasswordMatchesReferenceHashes(t *testing.T) {
	tests := []struct {
		name, raw, stored string
		want              bool
	}{
		{"ref 210k unicode", "Passw0rd-中文!", refHash210k, true},
		{"ref 210k wrong", "Passw0rd-中文", refHash210k, false},
		{"ref 1k custom iterations", "short", refHash1k, true},
		{"seeded plaintext", "plain", "plain", true},
		{"seeded plaintext wrong", "plain", "other", false},
		{"malformed", "x", "pbkdf2$abc$zzz$yyy", false},
		{"too few parts", "x", "pbkdf2$1000", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PasswordMatches(tt.raw, tt.stored); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

func TestHashPasswordFormatRoundTrip(t *testing.T) {
	h, err := HashPassword("Passw0rd-中文!")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(h, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" || parts[1] != "210000" || strings.Contains(h, "=") {
		t.Fatalf("format: %s", h)
	}
	if !IsHashed(h) || !PasswordMatches("Passw0rd-中文!", h) || PasswordMatches("nope", h) {
		t.Fatal("round trip")
	}
	// deterministic derive matches the reference vector
	salt := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	if got := formatHash(210000, salt, derive("Passw0rd-中文!", salt, 210000)); got != refHash210k {
		t.Fatalf("derive drift: %s", got)
	}
}

func TestSessionKeyAndBearer(t *testing.T) {
	if _, err := BearerToken(""); err == nil {
		t.Fatal("missing header")
	}
	if _, err := BearerToken("Basic abc"); err == nil {
		t.Fatal("wrong scheme")
	}
	if _, err := BearerToken("Bearer short"); err == nil {
		t.Fatal("short token")
	}
	tok := strings.Repeat("a", 43)
	got, err := BearerToken("Bearer  " + tok + " ")
	if err != nil || got != tok {
		t.Fatalf("%q %v", got, err)
	}
	// sha256("aaaa...") url-base64 no padding = 43 chars
	if k := SessionKey(tok); !strings.HasPrefix(k, "auth:session:") || len(k) != len("auth:session:")+43 {
		t.Fatalf("key %s", k)
	}
}

func TestSessionLifecycleAndThrottle(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	s := New(rdb, nil)
	ctx := context.Background()
	tok, err := s.CreateSession(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.ResolveUser(ctx, "Bearer "+tok)
	if err != nil || id != 42 {
		t.Fatalf("%d %v", id, err)
	}
	if err := s.RevokeSession(ctx, "Bearer "+tok); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveUser(ctx, "Bearer "+tok); err == nil {
		t.Fatal("revoked session must fail")
	}
	for i := 0; i < 8; i++ {
		if ok, _ := s.LoginAttemptAllowed(ctx, "bob"); !ok {
			t.Fatalf("attempt %d should be allowed", i)
		}
		_ = s.recordLoginFailure(ctx, "bob")
	}
	if ok, _ := s.LoginAttemptAllowed(ctx, "bob"); ok {
		t.Fatal("9th attempt must be throttled")
	}
	if ttl := mr.TTL("auth:login-failures:bob"); ttl <= 0 {
		t.Fatalf("failure counter needs a TTL, got %v", ttl)
	}
}

func TestRegisterNeedsInviteCodeWhenConfigured(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	s := New(rdb, nil)
	s.InviteCode = "letmein"
	name, pw := "alice", "password123"
	for _, code := range []*string{nil, ptr(""), ptr("wrong")} {
		resp, err := s.Register(context.Background(), model.AuthRequest{Username: &name, Password: &pw, InviteCode: code})
		if err != nil || resp.Code != 403 || resp.Msg != InviteRequiredMsg {
			t.Fatalf("code %v: got %+v %v", code, resp, err)
		}
	}
}

func ptr(s string) *string { return &s }
