// Package media owns media files: ingestion (upload, URL download), listing with a per-user cache,
// ownership checks, reference-counted deletion and audio export.
package media

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/agentclient"
	"dovideo/server/internal/checkpoint"
	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/redislock"
	"dovideo/server/internal/repo"
	"dovideo/server/internal/storage"
	"dovideo/server/internal/videocontext"
	"dovideo/server/internal/ytdlp"
)

const (
	md5KeyPrefix       = "media:md5:"
	evidenceObjectPref = "evidence-frames"
)

var videoSuffixes = map[string]bool{".mp4": true, ".mov": true, ".mkv": true, ".avi": true, ".webm": true, ".m4v": true}

// Service manages media files.
type Service struct {
	repo   *repo.Media
	locker *redislock.Locker
	rdb    redis.Cmdable
	minio  *storage.Minio
	cp     *checkpoint.Store
	agent  *agentclient.Client
	vctx   *videocontext.Service
	ytdlp  *ytdlp.Downloader
}

func NewService(r *repo.Media, rdb redis.Cmdable, locker *redislock.Locker, m *storage.Minio, cp *checkpoint.Store,
	agent *agentclient.Client, vctx *videocontext.Service, yt *ytdlp.Downloader) *Service {
	return &Service{repo: r, rdb: rdb, locker: locker, minio: m, cp: cp, agent: agent, vctx: vctx, ytdlp: yt}
}

// CalculateMD5 hashes a stream.
func CalculateMD5(r io.Reader) (string, error) {
	h := md5.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Service) RememberContentHash(ctx context.Context, mediaID int64, md5 string) {
	if md5 == "" {
		return
	}
	if err := s.rdb.Set(ctx, md5KeyPrefix+strconv.FormatInt(mediaID, 10), md5, 0).Err(); err != nil {
		slog.Warn("media_hash_cache_write_failed", "mediaId", mediaID, "err", err)
	}
}

// SaveUploadedMedia inserts the row; on failure the uploaded object is removed.
func (s *Service) SaveUploadedMedia(ctx context.Context, filename, fileURL string, userID int64, md5 string) (*model.MediaFile, error) {
	name, err := NormalizeVideoFilename(filename)
	if err != nil {
		s.removeUploadedObject(ctx, fileURL)
		return nil, err
	}
	m := &model.MediaFile{Filename: name, FilePath: fileURL, Status: "COMPLETED", UploadTime: model.LocalNow(), UserID: userID, ContentHash: &md5}
	if err := s.repo.Insert(ctx, m); err != nil {
		s.removeUploadedObject(ctx, fileURL)
		return nil, err
	}
	s.RememberContentHash(ctx, m.ID, md5)
	s.InvalidateUserList(ctx, userID)
	return m, nil
}

// SaveReference inserts a row that points at an existing object (instant upload). Unlike
// SaveUploadedMedia it never deletes the object on failure: the object belongs to other rows too.
func (s *Service) SaveReference(ctx context.Context, filename, fileURL string, userID int64, md5 string) (*model.MediaFile, error) {
	name, err := NormalizeVideoFilename(filename)
	if err != nil {
		return nil, err
	}
	m := &model.MediaFile{Filename: name, FilePath: fileURL, Status: "COMPLETED", UploadTime: model.LocalNow(), UserID: userID, ContentHash: &md5}
	if err := s.repo.Insert(ctx, m); err != nil {
		return nil, err
	}
	s.RememberContentHash(ctx, m.ID, md5)
	s.InvalidateUserList(ctx, userID)
	return m, nil
}

// FindByContentHash lists stored copies of a content hash.
func (s *Service) FindByContentHash(ctx context.Context, contentHash string) ([]model.MediaFile, error) {
	return s.repo.FindByContentHash(ctx, contentHash)
}

// ObjectLockKey serialises deletion of an object against new references to it: without it an
// instant upload could verify the object, a concurrent delete could then see zero references and
// remove it, and the new row would point at nothing.
func ObjectLockKey(contentHash string) string { return "lock:media-object:" + contentHash }

// ShouldDeleteObject is the reference-count rule: remove the stored object only when it is managed
// by us, no other row references it, and the count was taken under the object lock.
func ShouldDeleteObject(managed, locked bool, remainingRefs int64) bool {
	return managed && locked && remainingRefs == 0
}

func (s *Service) removeUploadedObject(ctx context.Context, fileURL string) {
	if err := s.minio.RemoveFile(ctx, fileURL); err != nil {
		slog.Warn("uploaded_object_rollback_failed", "path", fileURL, "err", err)
	}
}

func userListKey(userID int64) string { return "media:list:v2:user:" + strconv.FormatInt(userID, 10) }

// ListByUser serves the cached list (30 min) or queries the DB.
func (s *Service) ListByUser(ctx context.Context, userID int64) ([]model.MediaFile, error) {
	key := userListKey(userID)
	if cached, err := s.rdb.Get(ctx, key).Result(); err == nil {
		var out []model.MediaFile
		if jerr := json.Unmarshal([]byte(cached), &out); jerr == nil {
			return out, nil
		} else {
			slog.Warn("media_list_cache_read_failed", "userId", userID, "err", jerr)
		}
	} else if !errors.Is(err, redis.Nil) {
		slog.Warn("media_list_cache_read_failed", "userId", userID, "err", err)
	}
	list, err := s.repo.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []model.MediaFile{}
	}
	if b, err := common.MarshalNoEscape(list); err == nil {
		if err := s.rdb.Set(ctx, key, b, 30*time.Minute).Err(); err != nil {
			slog.Warn("media_list_cache_write_failed", "userId", userID, "err", err)
		}
	}
	return list, nil
}

// ContentHash returns the md5 (Redis cache first, DB fallback, re-cached). Empty if unknown.
func (s *Service) ContentHash(ctx context.Context, mediaID int64) (string, error) {
	if cached, err := s.rdb.Get(ctx, md5KeyPrefix+strconv.FormatInt(mediaID, 10)).Result(); err == nil && strings.TrimSpace(cached) != "" {
		return cached, nil
	} else if err != nil && !errors.Is(err, redis.Nil) {
		slog.Warn("media_hash_cache_read_failed", "mediaId", mediaID, "err", err)
	}
	m, err := s.repo.FindByID(ctx, mediaID)
	if err != nil {
		return "", err
	}
	if m == nil || m.ContentHash == nil {
		return "", nil
	}
	s.RememberContentHash(ctx, mediaID, *m.ContentHash)
	return *m.ContentHash, nil
}

// DeleteOwnedMedia removes the row, the stored object once nothing else references it, and all
// runtime artifacts. When the reference count cannot be established safely the object is kept:
// an orphaned object costs storage, a wrongly deleted one breaks another user's video.
func (s *Service) DeleteOwnedMedia(ctx context.Context, mediaID, userID int64) error {
	m, err := s.RequireOwnedMedia(ctx, mediaID, userID)
	if err != nil {
		return err
	}
	var lock *redislock.Lock
	locked := true
	if m.ContentHash != nil && *m.ContentHash != "" {
		lock, locked, err = s.locker.TryLock(ctx, ObjectLockKey(*m.ContentHash), 5*time.Second)
		if err != nil {
			locked = false
			slog.Warn("media_object_lock_failed", "mediaId", mediaID, "err", err)
		}
		defer func() {
			if lock.IsHeld() {
				_ = lock.Unlock(context.WithoutCancel(ctx))
			}
		}()
	}
	if err := s.repo.DeleteByID(ctx, mediaID); err != nil {
		return err
	}
	remaining, err := s.repo.CountFileReferences(ctx, m.ContentHash, m.FilePath)
	if err != nil {
		locked = false
		slog.Warn("media_object_refcount_failed", "mediaId", mediaID, "err", err)
	}
	if ShouldDeleteObject(s.minio.IsManagedFile(m.FilePath), locked, remaining) {
		if err := s.minio.RemoveFile(ctx, m.FilePath); err != nil {
			slog.Warn("media_object_cleanup_failed", "mediaId", mediaID, "path", m.FilePath, "err", err)
		}
	} else {
		slog.Info("media_object_retained", "mediaId", mediaID, "references", remaining, "locked", locked)
	}
	s.PurgeRuntimeArtifacts(ctx, mediaID)
	s.InvalidateUserList(ctx, userID)
	return nil
}

func (s *Service) Exists(ctx context.Context, mediaID int64) (bool, error) {
	m, err := s.repo.FindByID(ctx, mediaID)
	return m != nil, err
}

// PurgeRuntimeArtifacts removes evidence frames, Redis markers and (via the agent) checkpoints,
// telemetry and vectors. Failures are logged, never propagated.
func (s *Service) PurgeRuntimeArtifacts(ctx context.Context, mediaID int64) {
	vc, err := s.cp.LoadContext(ctx, mediaID)
	if err != nil {
		slog.Warn("media_evidence_manifest_read_failed", "mediaId", mediaID, "err", err)
	}
	s.vctx.DeleteEvidenceFrames(ctx, vc)
	id := strconv.FormatInt(mediaID, 10)
	if err := s.rdb.Del(ctx, md5KeyPrefix+id, "transcription:active:"+id, "transcription:state:"+id).Err(); err != nil {
		slog.Warn("media_runtime_cleanup_failed", "mediaId", mediaID, "err", err)
	}
	if err := s.agent.Purge(ctx, mediaID); err != nil {
		slog.Warn("media_runtime_cleanup_failed", "mediaId", mediaID, "err", err)
	}
}

func (s *Service) InvalidateUserList(ctx context.Context, userID int64) {
	if err := s.rdb.Del(ctx, userListKey(userID)).Err(); err != nil {
		slog.Warn("media_list_cache_invalidation_failed", "userId", userID, "err", err)
	}
}

// PlaybackURL returns a browser-reachable URL for the media source.
func (s *Service) PlaybackURL(ctx context.Context, source string) (string, error) {
	return s.minio.PlaybackURL(ctx, source)
}

func (s *Service) ReadableSource(ctx context.Context, source string) (string, error) {
	return s.minio.ReadableSource(ctx, source)
}

// NormalizeVideoFilename validates and strips directories from the uploaded name.
func NormalizeVideoFilename(filename string) (string, error) {
	if strings.TrimSpace(filename) == "" {
		return "", common.InvalidArgument("视频文件名不能为空")
	}
	n := strings.ReplaceAll(filename, "\\", "/")
	n = strings.TrimSpace(n[strings.LastIndex(n, "/")+1:])
	if n == "" || utf16Len(n) > 255 {
		return "", common.InvalidArgument("视频文件名无效或过长")
	}
	if !videoSuffixes[strings.ToLower(fileSuffix(n))] {
		return "", common.InvalidArgument("仅支持 MP4、MOV、MKV、AVI、WEBM 和 M4V 视频")
	}
	return n, nil
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func fileSuffix(filename string) string {
	if dot := strings.LastIndex(filename, "."); dot >= 0 {
		return filename[dot:]
	}
	return ""
}

// RequireOwnedMedia loads a media row owned by userID (404 / 403 errors otherwise).
func (s *Service) RequireOwnedMedia(ctx context.Context, mediaID, userID int64) (*model.MediaFile, error) {
	m, err := s.repo.FindByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, common.NotFound("文件不存在")
	}
	if m.UserID != userID {
		return nil, common.Forbidden("无权访问该文件")
	}
	return m, nil
}

// IngestFile stores a multipart upload.
func (s *Service) IngestFile(ctx context.Context, fh *multipart.FileHeader, userID int64) (*model.MediaFile, error) {
	if fh == nil || fh.Size == 0 {
		return nil, common.InvalidArgument("上传文件不能为空")
	}
	filename, err := NormalizeVideoFilename(fh.Filename)
	if err != nil {
		return nil, err
	}
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	md5sum, err := CalculateMD5(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	f, err = fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fileURL, err := s.minio.UploadStream(ctx, f, fh.Size, fh.Filename, fh.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	return s.SaveUploadedMedia(ctx, filename, fileURL, userID, md5sum)
}

// IngestURL downloads a video with yt-dlp and stores it.
func (s *Service) IngestURL(ctx context.Context, url string, userID int64) (*model.MediaFile, error) {
	if strings.TrimSpace(url) == "" {
		return nil, common.InvalidArgument("视频链接不能为空")
	}
	tmp, title, err := s.ytdlp.Download(ctx, url)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
			slog.Warn("temporary_video_cleanup_failed", "path", tmp)
		}
	}()
	f, err := os.Open(tmp)
	if err != nil {
		return nil, err
	}
	md5sum, err := CalculateMD5(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	fileURL, err := s.minio.UploadLocalFile(ctx, tmp, filepath.Base(tmp), "")
	if err != nil {
		return nil, err
	}
	name := "WEB_" + filepath.Base(tmp)
	if t := linkTitle(title); t != "" {
		name = t + ".mp4"
	}
	return s.SaveUploadedMedia(ctx, name, fileURL, userID, md5sum)
}

// linkTitle makes a site's video title usable as a file name: no path separators or control characters, at most
// 80 characters.
func linkTitle(title string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(title) {
		if n >= 80 {
			break
		}
		switch {
		case r == '/' || r == '\\' || r < 0x20 || r == 0x7f:
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
		n++
	}
	return strings.TrimSpace(b.String())
}

// ExportMP3 extracts the audio track to a temporary MP3 file and returns its path.
func (s *Service) ExportMP3(ctx context.Context, m *model.MediaFile) (string, error) {
	input, err := s.ReadableSource(ctx, m.FilePath)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(input) == "" || (!strings.HasPrefix(input, "http") && !isRegular(input)) {
		return "", common.NotFound("视频源文件不存在")
	}
	out, err := os.CreateTemp("", "dovideo-audio-*.mp3")
	if err != nil {
		return "", common.Internal("音频转换失败", err)
	}
	out.Close()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "ffmpeg", "-y", "-i", input, "-vn", "-acodec", "libmp3lame", "-q:a", "2", out.Name())
	err = cmd.Run()
	if cctx.Err() != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		_ = os.Remove(out.Name())
		return "", common.Internal("音频转换超时", nil)
	}
	if err != nil {
		_ = os.Remove(out.Name())
		return "", common.Internal("音频转换失败", err)
	}
	return out.Name(), nil
}

func isRegular(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}
