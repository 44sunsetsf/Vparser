// Package upload implements resumable chunked uploads (chunks staged in MinIO, progress in Redis,
// merge under a distributed lock and made idempotent by a completion marker) and instant upload.
package upload

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/common"
	"dovideo/server/internal/media"
	"dovideo/server/internal/model"
	"dovideo/server/internal/redislock"
	"dovideo/server/internal/storage"
)

const (
	uploadKeyPrefix   = "upload:chunked:"
	MaxChunkBytes     = 5 * 1024 * 1024
	MaxTotalChunks    = 410
	chunkObjectPrefix = "chunk-uploads/"
	uploadTTL         = 24 * time.Hour
)

// Service handles chunked uploads.
type Service struct {
	rdb    redis.Cmdable
	locker *redislock.Locker
	minio  *storage.Minio
	media  *media.Service
}

func New(rdb redis.Cmdable, locker *redislock.Locker, m *storage.Minio, ms *media.Service) *Service {
	return &Service{rdb: rdb, locker: locker, minio: m, media: ms}
}

func uploadKey(id string) string    { return uploadKeyPrefix + id }
func partsKey(id string) string     { return uploadKey(id) + ":parts" }
func completedKey(id string) string { return uploadKey(id) + ":completed" }

func chunkObjectName(id string, idx int) (string, error) {
	if err := validateUploadID(id); err != nil {
		return "", err
	}
	return chunkObjectPrefix + id + "/part-" + strconv.Itoa(idx), nil
}

func validateUploadID(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return common.InvalidArgument("invalid uploadId")
	}
	return nil
}

// Initialize creates upload metadata and returns the uploadId.
func (s *Service) Initialize(ctx context.Context, filename string, totalChunks int, userID int64) (string, error) {
	name, err := media.NormalizeVideoFilename(filename)
	if err != nil {
		return "", err
	}
	if totalChunks <= 0 || totalChunks > MaxTotalChunks {
		return "", common.InvalidArgument(fmt.Sprintf("totalChunks must be between 1 and %d", MaxTotalChunks))
	}
	id := uuid.NewString()
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, uploadKey(id), map[string]any{
		"filename": name, "totalChunks": strconv.Itoa(totalChunks), "userId": strconv.FormatInt(userID, 10),
	})
	pipe.Expire(ctx, uploadKey(id), uploadTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// UploadedChunks returns the sorted set of received chunk indexes.
func (s *Service) UploadedChunks(ctx context.Context, uploadID string, userID int64) ([]int, error) {
	if _, err := s.requireUpload(ctx, uploadID, userID); err != nil {
		return nil, err
	}
	members, err := s.rdb.SMembers(ctx, partsKey(uploadID)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(members))
	for _, m := range members {
		n, err := strconv.Atoi(m)
		if err != nil {
			return nil, common.InvalidArgument(fmt.Sprintf("invalid number %q", m))
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

// UploadChunk stores one chunk in MinIO and marks it received.
func (s *Service) UploadChunk(ctx context.Context, uploadID string, chunkIndex, totalChunks int,
	fh *multipart.FileHeader, userID int64) error {
	if fh == nil || fh.Size == 0 {
		return common.InvalidArgument("chunk is empty")
	}
	if fh.Size > MaxChunkBytes {
		return common.InvalidArgument("chunk size cannot exceed 5MB")
	}
	meta, err := s.requireUpload(ctx, uploadID, userID)
	if err != nil {
		return err
	}
	expected, err := strconv.Atoi(meta["totalChunks"])
	if err != nil {
		return common.InvalidArgument(fmt.Sprintf("invalid number %q", meta["totalChunks"]))
	}
	if totalChunks != expected || chunkIndex < 0 || chunkIndex >= expected {
		return common.InvalidArgument("invalid chunk index or totalChunks")
	}
	name, err := chunkObjectName(uploadID, chunkIndex)
	if err != nil {
		return err
	}
	f, err := fh.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := s.minio.UploadObject(ctx, name, f, fh.Size, "application/octet-stream"); err != nil {
		return err
	}
	pipe := s.rdb.Pipeline()
	pipe.SAdd(ctx, partsKey(uploadID), strconv.Itoa(chunkIndex))
	pipe.Expire(ctx, uploadKey(uploadID), uploadTTL)
	pipe.Expire(ctx, partsKey(uploadID), uploadTTL)
	_, err = pipe.Exec(ctx)
	return err
}

// Complete merges the chunks under lock:upload:merge:{uploadId} (idempotent via the completed key).
func (s *Service) Complete(ctx context.Context, uploadID string, userID int64) (*model.MediaFile, error) {
	if err := validateUploadID(uploadID); err != nil {
		return nil, err
	}
	lock, ok, err := s.locker.TryLock(ctx, "lock:upload:merge:"+uploadID, 0)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, common.Business(common.CodeConflict, "该上传任务正在合并中，请稍后重试")
	}
	defer func() {
		if lock.IsHeld() {
			if err := lock.Unlock(context.WithoutCancel(ctx)); err != nil {
				slog.Warn("upload_merge_unlock_failed", "uploadId", uploadID, "err", err)
			}
		}
	}()

	if done, err := s.completedUpload(ctx, uploadID, userID); err != nil || done != nil {
		return done, err
	}
	meta, err := s.requireUpload(ctx, uploadID, userID)
	if err != nil {
		return nil, err
	}
	filename := meta["filename"]
	totalChunks, err := strconv.Atoi(meta["totalChunks"])
	if err != nil {
		return nil, common.InvalidArgument(fmt.Sprintf("invalid number %q", meta["totalChunks"]))
	}
	uploaded, err := s.UploadedChunks(ctx, uploadID, userID)
	if err != nil {
		return nil, err
	}
	if len(uploaded) != totalChunks {
		return nil, common.Business(common.CodeConflict,
			fmt.Sprintf("分片尚未全部上传完成（已传 %d/%d）", len(uploaded), totalChunks))
	}
	suffix := ""
	if dot := strings.LastIndex(filename, "."); dot >= 0 {
		suffix = filename[dot:]
	}
	merged, err := os.CreateTemp("", "dovideo-merged-*"+suffix)
	if err != nil {
		return nil, err
	}
	defer os.Remove(merged.Name())
	digest := md5.New()
	bw := bufio.NewWriter(io.MultiWriter(merged, digest))
	for i := 0; i < totalChunks; i++ {
		name, err := chunkObjectName(uploadID, i)
		if err != nil {
			merged.Close()
			return nil, err
		}
		if err := s.minio.CopyObjectTo(ctx, name, bw); err != nil {
			merged.Close()
			return nil, err
		}
	}
	if err := bw.Flush(); err != nil {
		merged.Close()
		return nil, err
	}
	if err := merged.Close(); err != nil {
		return nil, err
	}
	fileURL, err := s.minio.UploadLocalFile(ctx, merged.Name(), filename, "")
	if err != nil {
		return nil, err
	}
	mf, err := s.media.SaveUploadedMedia(ctx, filename, fileURL, userID, hex.EncodeToString(digest.Sum(nil)))
	if err != nil {
		return nil, err
	}
	// record success first, then clean up: retries never insert a second media row
	if err := s.rdb.Set(ctx, completedKey(uploadID), strconv.FormatInt(mf.ID, 10), uploadTTL).Err(); err != nil {
		return nil, err
	}
	s.cleanup(ctx, uploadID, totalChunks, mf.ID)
	return mf, nil
}

func (s *Service) completedUpload(ctx context.Context, uploadID string, userID int64) (*model.MediaFile, error) {
	v, err := s.rdb.Get(ctx, completedKey(uploadID)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		s.rdb.Del(ctx, completedKey(uploadID))
		return nil, nil
	}
	return s.media.RequireOwnedMedia(ctx, id, userID)
}

func (s *Service) requireUpload(ctx context.Context, uploadID string, userID int64) (map[string]string, error) {
	if err := validateUploadID(uploadID); err != nil {
		return nil, err
	}
	meta, err := s.rdb.HGetAll(ctx, uploadKey(uploadID)).Result()
	if err != nil {
		return nil, err
	}
	if len(meta) == 0 {
		return nil, common.InvalidArgument("uploadId does not exist or has expired")
	}
	if strconv.FormatInt(userID, 10) != meta["userId"] {
		return nil, common.Forbidden("无权访问该上传任务")
	}
	return meta, nil
}

func (s *Service) cleanup(ctx context.Context, uploadID string, total int, mediaID int64) {
	for i := 0; i < total; i++ {
		name, _ := chunkObjectName(uploadID, i)
		if err := s.minio.RemoveObject(ctx, name); err != nil {
			slog.Warn("chunk_object_cleanup_failed", "uploadId", uploadID, "chunkIndex", i, "mediaId", mediaID, "err", err)
		}
	}
	if err := s.rdb.Del(ctx, uploadKey(uploadID), partsKey(uploadID)).Err(); err != nil {
		slog.Warn("chunk_upload_metadata_cleanup_failed", "uploadId", uploadID, "mediaId", mediaID, "err", err)
	}
}
