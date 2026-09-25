// Package auth implements accounts and sessions: PBKDF2-SHA256 password hashing, opaque bearer
// tokens stored in Redis only as their SHA-256 (a Redis dump does not leak usable tokens), and
// per-username login throttling.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/pbkdf2"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/repo"
)

const (
	passwordPrefix     = "pbkdf2"
	passwordIterations = 210_000
	passwordKeyBytes   = 32
	saltBytes          = 16
	tokenBytes         = 32
	sessionTTL         = 24 * time.Hour
	sessionPrefix      = "auth:session:"
	loginFailurePrefix = "auth:login-failures:"
	maxLoginFailures   = 8
	loginFailureWindow = 10 * time.Minute
	maxPasswordLength  = 128
	maxNicknameLength  = 50
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`)

// Service is the authentication service.
type Service struct {
	rdb   redis.Cmdable
	users *repo.Users
	// InviteCode, when set, closes open registration: only people given the code can sign up.
	InviteCode string
}

func New(rdb redis.Cmdable, users *repo.Users) *Service { return &Service{rdb: rdb, users: users} }

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func response(code int, msg string, user *model.UserInfo, token string) model.AuthResponse {
	return model.AuthResponse{Code: code, Msg: msg, UserInfo: user, Token: token}
}

func userView(u *model.User) *model.UserInfo {
	return &model.UserInfo{ID: u.ID, Username: u.Username, Nickname: u.Nickname, Avatar: u.Avatar, Role: u.Role}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// InviteRequiredMsg is what registration answers without a valid invite code.
const InviteRequiredMsg = "项目内测中，请联系作者索要内测码"

func normalizeUsername(username *string) (string, bool) {
	if username == nil {
		return "", false
	}
	n := strings.TrimSpace(*username)
	return n, usernameRe.MatchString(n)
}

// normalizeNickname returns ("", true) for blank, (nickname, true) when valid, ("", false) if too long.
func normalizeNickname(nickname *string) (string, bool) {
	if nickname == nil || strings.TrimSpace(*nickname) == "" {
		return "", true
	}
	n := strings.TrimSpace(*nickname)
	return n, utf16Len(n) <= maxNicknameLength
}

// Register creates an account. Validation failures are reported in the response; infrastructure
// failures return an error.
func (s *Service) Register(ctx context.Context, req model.AuthRequest) (model.AuthResponse, error) {
	if s.InviteCode != "" && (req.InviteCode == nil ||
		subtle.ConstantTimeCompare([]byte(strings.TrimSpace(*req.InviteCode)), []byte(s.InviteCode)) != 1) {
		return response(403, InviteRequiredMsg, nil, ""), nil
	}
	username, ok := normalizeUsername(req.Username)
	password := deref(req.Password)
	if !ok || req.Password == nil || utf16Len(password) < 8 || utf16Len(password) > maxPasswordLength {
		return response(400, "账号需为 3-32 位字母、数字或下划线，密码需为 8-128 位", nil, ""), nil
	}
	nickname, ok := normalizeNickname(req.Nickname)
	if !ok {
		return response(400, "昵称不能超过 50 个字符", nil, ""), nil
	}
	count, err := s.users.Count(ctx, username)
	if err != nil {
		return model.AuthResponse{}, err
	}
	if count > 0 {
		return response(409, "该账号已存在", nil, ""), nil
	}
	if nickname == "" {
		nickname = "用户" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	}
	hashed, err := HashPassword(password)
	if err != nil {
		return model.AuthResponse{}, err
	}
	user := &model.User{Username: username, Password: hashed, Nickname: nickname, Role: "USER"}
	if err := s.users.Create(ctx, user); err != nil {
		if errors.Is(err, repo.ErrDuplicate) {
			return response(409, "该账号已存在", nil, ""), nil
		}
		return model.AuthResponse{}, err
	}
	slog.Info("user_registered", "userId", user.ID, "username", username)
	return response(200, "注册成功", userView(user), ""), nil
}

// Login verifies credentials and issues a session token.
func (s *Service) Login(ctx context.Context, req model.AuthRequest) (model.AuthResponse, error) {
	username, ok := normalizeUsername(req.Username)
	if !ok || req.Password == nil || strings.TrimSpace(*req.Password) == "" {
		return response(400, "请输入账号和密码", nil, ""), nil
	}
	allowed, err := s.LoginAttemptAllowed(ctx, username)
	if err != nil {
		return model.AuthResponse{}, err
	}
	if !allowed {
		return response(429, "登录尝试过于频繁，请稍后再试", nil, ""), nil
	}
	user, err := s.users.FindByUsername(ctx, username)
	if err != nil {
		return model.AuthResponse{}, err
	}
	if user == nil || !PasswordMatches(*req.Password, user.Password) {
		if err := s.recordLoginFailure(ctx, username); err != nil {
			return model.AuthResponse{}, err
		}
		return response(401, "账号或密码错误", nil, ""), nil
	}
	if !IsHashed(user.Password) {
		// accounts seeded directly in SQL (e.g. a bootstrap admin) carry a plaintext password; it is
		// rehashed on the first successful login
		hashed, err := HashPassword(*req.Password)
		if err != nil {
			return model.AuthResponse{}, err
		}
		if err := s.users.UpdatePassword(ctx, user.ID, hashed); err != nil {
			return model.AuthResponse{}, err
		}
	}
	if err := s.rdb.Del(ctx, loginFailurePrefix+username).Err(); err != nil {
		return model.AuthResponse{}, err
	}
	token, err := s.CreateSession(ctx, user.ID)
	if err != nil {
		return model.AuthResponse{}, err
	}
	slog.Info("user_logged_in", "userId", user.ID)
	return response(200, "登录成功", userView(user), token), nil
}

// RequireAdmin rejects non-admin users.
func (s *Service) RequireAdmin(ctx context.Context, userID int64) error {
	u, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return err
	}
	if u == nil || u.Role != "ADMIN" {
		return common.Forbidden("仅管理员可操作失败任务")
	}
	return nil
}

// HashPassword returns pbkdf2$iterations$salt$hash (base64 without padding, std alphabet).
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return formatHash(passwordIterations, salt, derive(password, salt, passwordIterations)), nil
}

func formatHash(iterations int, salt, hash []byte) string {
	enc := base64.RawStdEncoding
	return strings.Join([]string{passwordPrefix, strconv.Itoa(iterations), enc.EncodeToString(salt), enc.EncodeToString(hash)}, "$")
}

func derive(password string, salt []byte, iterations int) []byte {
	return pbkdf2.Key([]byte(password), salt, iterations, passwordKeyBytes, sha256.New)
}

// IsHashed reports whether the stored value uses the pbkdf2 format.
func IsHashed(stored string) bool { return strings.HasPrefix(stored, passwordPrefix+"$") }

// PasswordMatches verifies a password against a stored hash (or a SQL-seeded plaintext value).
func PasswordMatches(raw, stored string) bool {
	if !IsHashed(stored) {
		return subtle.ConstantTimeCompare([]byte(raw), []byte(stored)) == 1
	}
	parts := strings.Split(stored, "$")
	if len(parts) < 4 {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	actual := pbkdf2.Key([]byte(raw), salt, iterations, len(expected), sha256.New)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// CreateSession issues a 32-byte URL-safe token and stores userId under its SHA-256 key for 24h.
func (s *Service) CreateSession(ctx context.Context, userID int64) (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	if err := s.rdb.Set(ctx, SessionKey(token), strconv.FormatInt(userID, 10), sessionTTL).Err(); err != nil {
		return "", err
	}
	return token, nil
}

// ResolveUser maps an Authorization header to a user id (Security errors -> 401 upstream).
func (s *Service) ResolveUser(ctx context.Context, authorization string) (int64, error) {
	token, err := BearerToken(authorization)
	if err != nil {
		return 0, err
	}
	v, err := s.rdb.Get(ctx, SessionKey(token)).Result()
	if errors.Is(err, redis.Nil) {
		return 0, common.Forbidden("登录状态已失效")
	}
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, common.InvalidArgument(fmt.Sprintf("invalid number %q", v))
	}
	return id, nil
}

func (s *Service) RevokeSession(ctx context.Context, authorization string) error {
	token, err := BearerToken(authorization)
	if err != nil {
		return err
	}
	return s.rdb.Del(ctx, SessionKey(token)).Err()
}

// LoginAttemptAllowed reports whether fewer than 8 failures were recorded in the window.
func (s *Service) LoginAttemptAllowed(ctx context.Context, username string) (bool, error) {
	v, err := s.rdb.Get(ctx, loginFailurePrefix+username).Result()
	if errors.Is(err, redis.Nil) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	n, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		s.rdb.Del(ctx, loginFailurePrefix+username)
		return true, nil
	}
	return n < maxLoginFailures, nil
}

func (s *Service) recordLoginFailure(ctx context.Context, username string) error {
	key := loginFailurePrefix + username
	n, err := s.rdb.Incr(ctx, key).Result()
	if err != nil {
		return err
	}
	if n == 1 {
		return s.rdb.Expire(ctx, key, loginFailureWindow).Err()
	}
	return nil
}

// BearerToken extracts the token; errors are Security (-> 401 via the auth interceptor).
func BearerToken(authorization string) (string, error) {
	if !strings.HasPrefix(authorization, "Bearer ") {
		return "", common.Forbidden("请先登录")
	}
	token := strings.TrimSpace(authorization[len("Bearer "):])
	if utf16Len(token) < 32 {
		return "", common.Forbidden("无效的登录凭证")
	}
	return token, nil
}

// SessionKey is auth:session:<base64url(sha256(token))>.
func SessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return sessionPrefix + base64.RawURLEncoding.EncodeToString(sum[:])
}
