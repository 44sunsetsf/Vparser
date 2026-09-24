package upload

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/common"
	"dovideo/server/internal/media"
	"dovideo/server/internal/model"
	"dovideo/server/internal/redislock"
)

// Instant upload ("秒传", contract §9).
//
// Knowing a file's MD5 is not proof of having the file: MD5s leak (logs, share links, public
// datasets). The server therefore answers a hash match with a challenge — the MD5 of a random byte
// range of at most 1 MiB — that only a client holding the actual bytes can answer. The range is
// chosen server side, stored in a single-use Redis hash for two minutes, and verified with one
// ranged GET against the stored object.

const (
	maxChallengeBytes  = 1 << 20
	challengeTTL       = 2 * time.Minute
	challengeKeyPrefix = "upload:challenge:"
	objectLockWait     = 5 * time.Second
)

var md5Hex = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)

// Challenge is returned when an identical object exists.
type Challenge struct {
	ChallengeID string `json:"challengeId"`
	Offset      int64  `json:"offset"`
	Length      int64  `json:"length"`
}

// ChallengeRequest is the body of POST /media/instant-upload/challenge.
type ChallengeRequest struct {
	MD5      string `json:"md5"`
	Size     int64  `json:"size"`
	Filename string `json:"filename"`
}

// ProofRequest is the body of POST /media/instant-upload.
type ProofRequest struct {
	ChallengeID string `json:"challengeId"`
	RangeMD5    string `json:"rangeMd5"`
}

// ObjectStore is the object-store surface instant upload needs.
type ObjectStore interface {
	IsManagedFile(fileURL string) bool
	ObjectSize(ctx context.Context, fileURL string) (int64, error)
	ReadRange(ctx context.Context, fileURL string, offset, length int64) ([]byte, error)
}

// MediaCopies finds existing copies and records new references.
type MediaCopies interface {
	FindByContentHash(ctx context.Context, contentHash string) ([]model.MediaFile, error)
	SaveReference(ctx context.Context, filename, fileURL string, userID int64, md5 string) (*model.MediaFile, error)
}

// Instant implements the challenge/proof protocol.
type Instant struct {
	rdb    redis.Cmdable
	locker *redislock.Locker
	store  ObjectStore
	media  MediaCopies
	// randInt returns a uniform value in [0, n); crypto/rand so ranges cannot be predicted.
	randInt func(n int64) (int64, error)
}

func NewInstant(rdb redis.Cmdable, locker *redislock.Locker, store ObjectStore, m MediaCopies) *Instant {
	return &Instant{rdb: rdb, locker: locker, store: store, media: m, randInt: cryptoRandInt}
}

func cryptoRandInt(n int64) (int64, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0, err
	}
	return v.Int64(), nil
}

// ValidateChallengeRequest normalises and checks a challenge request.
func ValidateChallengeRequest(req ChallengeRequest) (ChallengeRequest, error) {
	if !md5Hex.MatchString(req.MD5) {
		return req, common.InvalidArgument("md5 必须是 32 位十六进制字符串")
	}
	if req.Size <= 0 {
		return req, common.InvalidArgument("文件大小无效")
	}
	name, err := media.NormalizeVideoFilename(req.Filename)
	if err != nil {
		return req, err
	}
	return ChallengeRequest{MD5: strings.ToLower(req.MD5), Size: req.Size, Filename: name}, nil
}

// ChallengeRange picks the byte range to prove: min(size, 1 MiB) bytes at a random offset.
func ChallengeRange(size int64, randInt func(int64) (int64, error)) (offset, length int64, err error) {
	length = min(size, maxChallengeBytes)
	span := size - length + 1 // number of valid offsets
	if span <= 1 {
		return 0, length, nil
	}
	offset, err = randInt(span)
	return offset, length, err
}

// VerifyRange reports whether data hashes to the claimed MD5 (constant-time comparison).
func VerifyRange(data []byte, claimedHex string) bool {
	claimed, err := hex.DecodeString(strings.ToLower(claimedHex))
	if err != nil || len(claimed) != md5.Size {
		return false
	}
	sum := md5.Sum(data)
	return subtle.ConstantTimeCompare(sum[:], claimed) == 1
}

// Challenge returns nil when no stored object has the same MD5 and size (the client then falls back
// to a chunked upload), otherwise a single-use challenge bound to the calling user.
func (s *Instant) Challenge(ctx context.Context, userID int64, raw ChallengeRequest) (*Challenge, error) {
	req, err := ValidateChallengeRequest(raw)
	if err != nil {
		return nil, err
	}
	copies, err := s.media.FindByContentHash(ctx, req.MD5)
	if err != nil {
		return nil, err
	}
	var source string
	for _, c := range copies {
		if !s.store.IsManagedFile(c.FilePath) {
			continue
		}
		size, err := s.store.ObjectSize(ctx, c.FilePath)
		if err != nil {
			slog.Warn("instant_upload_candidate_unreadable", "mediaId", c.ID, "err", err)
			continue
		}
		if size == req.Size {
			source = c.FilePath
			break
		}
	}
	if source == "" {
		return nil, nil
	}
	offset, length, err := ChallengeRange(req.Size, s.randInt)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	key := challengeKeyPrefix + id
	pipe := s.rdb.TxPipeline()
	pipe.HSet(ctx, key, map[string]any{
		"userId": strconv.FormatInt(userID, 10), "md5": req.MD5, "filename": req.Filename, "filePath": source,
		"offset": strconv.FormatInt(offset, 10), "length": strconv.FormatInt(length, 10),
	})
	pipe.Expire(ctx, key, challengeTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	return &Challenge{ChallengeID: id, Offset: offset, Length: length}, nil
}

// takeChallenge reads and deletes the challenge in one transaction, so a proof can be attempted
// once: a wrong guess burns the challenge instead of allowing an online brute force.
func (s *Instant) takeChallenge(ctx context.Context, id string) (map[string]string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, common.InvalidArgument("challengeId 无效")
	}
	key := challengeKeyPrefix + id
	pipe := s.rdb.TxPipeline()
	get := pipe.HGetAll(ctx, key)
	pipe.Del(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	if len(get.Val()) == 0 {
		return nil, common.InvalidArgument("秒传校验已过期，请重新上传")
	}
	return get.Val(), nil
}

// Prove verifies the answer and, on success, creates the caller's media row pointing at the
// existing object.
func (s *Instant) Prove(ctx context.Context, userID int64, req ProofRequest) (*model.MediaFile, error) {
	if !md5Hex.MatchString(req.RangeMD5) {
		return nil, common.InvalidArgument("rangeMd5 必须是 32 位十六进制字符串")
	}
	ch, err := s.takeChallenge(ctx, req.ChallengeID)
	if err != nil {
		return nil, err
	}
	if ch["userId"] != strconv.FormatInt(userID, 10) {
		return nil, common.Forbidden("无权使用该秒传校验")
	}
	offset, err1 := strconv.ParseInt(ch["offset"], 10, 64)
	length, err2 := strconv.ParseInt(ch["length"], 10, 64)
	if err1 != nil || err2 != nil || length <= 0 {
		return nil, common.Internal("秒传校验状态损坏", nil)
	}
	// Hold the object lock from verification to insert so a concurrent delete cannot remove the
	// object between the two (see media.ObjectLockKey).
	lock, ok, err := s.locker.TryLock(ctx, media.ObjectLockKey(ch["md5"]), objectLockWait)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, common.Business(common.CodeConflict, "文件正在被处理，请稍后重试")
	}
	defer func() { _ = lock.Unlock(context.WithoutCancel(ctx)) }()
	data, err := s.store.ReadRange(ctx, ch["filePath"], offset, length)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != length || !VerifyRange(data, req.RangeMD5) {
		slog.Warn("instant_upload_proof_rejected", "userId", userID)
		return nil, common.InvalidArgument("秒传校验失败，请改用普通上传")
	}
	return s.media.SaveReference(ctx, ch["filename"], ch["filePath"], userID, ch["md5"])
}
