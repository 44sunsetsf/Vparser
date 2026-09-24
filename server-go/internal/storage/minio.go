// Package storage wraps MinIO: object naming, uploads, ranged reads and presigned URLs.
package storage

import (
	"context"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"dovideo/server/internal/common"
	"dovideo/server/internal/config"
)

// Minio wraps the client with the bucket/endpoint the URLs are built from.
type Minio struct {
	client *minio.Client
	// public presigns playback URLs against the browser-reachable endpoint. Inside Docker the internal
	// endpoint (http://minio:9000) is not resolvable by the browser, which broke video playback.
	public   *minio.Client
	bucket   string
	endpoint string // without trailing slash
}

var suffixRe = regexp.MustCompile(`^\.[a-z0-9]+$`)

// Go's built-in MIME table has no video/audio types and slim images ship no /etc/mime.types, so merged
// uploads were stored as application/octet-stream. Register the common media types explicitly.
func init() {
	for ext, typ := range map[string]string{
		".mp4": "video/mp4", ".m4v": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm",
		".mkv": "video/x-matroska", ".avi": "video/x-msvideo", ".flv": "video/x-flv", ".ts": "video/mp2t",
		".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".wav": "audio/wav", ".aac": "audio/aac", ".ogg": "audio/ogg",
	} {
		if mime.TypeByExtension(ext) == "" {
			_ = mime.AddExtensionType(ext, typ)
		}
	}
}

// New connects and creates the bucket if missing.
func New(ctx context.Context, c *config.Config) (*Minio, error) {
	u, err := url.Parse(c.MinioEndpoint)
	if err != nil || u.Host == "" {
		return nil, common.Internal("MinIO 初始化失败", err)
	}
	base, err := minio.DefaultTransport(u.Scheme == "https")
	if err != nil {
		return nil, common.Internal("MinIO 初始化失败", err)
	}
	// every object-store call becomes a client span of the request or task that issued it
	traced := otelhttp.NewTransport(base, otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
		return "minio " + r.Method
	}))
	cli, err := minio.New(u.Host, &minio.Options{
		Creds:     credentials.NewStaticV4(c.MinioAccessKey, c.MinioSecretKey, ""),
		Secure:    u.Scheme == "https",
		Transport: traced,
	})
	if err != nil {
		return nil, common.Internal("MinIO 初始化失败", err)
	}
	exists, err := cli.BucketExists(ctx, c.MinioBucket)
	if err != nil {
		return nil, common.Internal("MinIO 初始化失败", err)
	}
	if !exists {
		if err := cli.MakeBucket(ctx, c.MinioBucket, minio.MakeBucketOptions{}); err != nil {
			return nil, common.Internal("MinIO 初始化失败", err)
		}
		slog.Info("minio_bucket_created", "bucket", c.MinioBucket)
	}
	// Whole-bucket public-read is deliberately ignored (media is served via short-lived presigned URLs).
	if c.MinioPublicRead {
		slog.Warn("minio_public_read_ignored", "bucket", c.MinioBucket, "reason", "media_served_via_short_lived_presigned_url")
	}
	public := cli
	if pe := strings.TrimSpace(c.MinioPublicEndpoint); pe != "" && strings.TrimRight(pe, "/") != strings.TrimRight(c.MinioEndpoint, "/") {
		pu, err := url.Parse(pe)
		if err != nil || pu.Host == "" {
			return nil, common.Internal("MINIO_PUBLIC_ENDPOINT 无效", err)
		}
		// Region is fixed so presigning never needs a network round-trip to the (unreachable) public host.
		public, err = minio.New(pu.Host, &minio.Options{
			Creds:  credentials.NewStaticV4(c.MinioAccessKey, c.MinioSecretKey, ""),
			Secure: pu.Scheme == "https",
			Region: "us-east-1",
		})
		if err != nil {
			return nil, common.Internal("MinIO 初始化失败", err)
		}
	}
	return &Minio{client: cli, public: public, bucket: c.MinioBucket, endpoint: strings.TrimRight(c.MinioEndpoint, "/")}, nil
}

func fileSuffix(filename string) string {
	dot := strings.LastIndex(filename, ".")
	if dot < 0 || len(filename)-dot > 11 {
		return ""
	}
	s := strings.ToLower(filename[dot:])
	if suffixRe.MatchString(s) {
		return s
	}
	return ""
}

func validateObjectName(name string) error {
	if strings.TrimSpace(name) == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return common.InvalidArgument("invalid MinIO object name")
	}
	return nil
}

func (m *Minio) ObjectURL(objectName string) (string, error) {
	if err := validateObjectName(objectName); err != nil {
		return "", err
	}
	return m.endpoint + "/" + m.bucket + "/" + objectName, nil
}

// UploadStream stores a multipart file under a random name with a normalised suffix.
func (m *Minio) UploadStream(ctx context.Context, r io.Reader, size int64, originalFilename, contentType string) (string, error) {
	objectName := uuid.NewString() + fileSuffix(originalFilename)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if _, err := m.client.PutObject(ctx, m.bucket, objectName, r, size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return "", err
	}
	return m.ObjectURL(objectName)
}

// UploadLocalFile stores a local file under objectPrefix/ with a random name.
func (m *Minio) UploadLocalFile(ctx context.Context, path, originalFilename, objectPrefix string) (string, error) {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return "", common.InvalidArgument("local file does not exist")
	}
	prefix := strings.Trim(objectPrefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	objectName := prefix + uuid.NewString() + fileSuffix(originalFilename)
	if err := validateObjectName(objectName); err != nil {
		return "", err
	}
	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := m.client.PutObject(ctx, m.bucket, objectName, f, st.Size(), minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return "", err
	}
	return m.ObjectURL(objectName)
}

// UploadObject stores under an explicit object name (chunk parts).
func (m *Minio) UploadObject(ctx context.Context, objectName string, r io.Reader, size int64, contentType string) (string, error) {
	if err := validateObjectName(objectName); err != nil {
		return "", err
	}
	if _, err := m.client.PutObject(ctx, m.bucket, objectName, r, size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
		return "", err
	}
	return m.ObjectURL(objectName)
}

// CopyObjectTo streams an object into w.
func (m *Minio) CopyObjectTo(ctx context.Context, objectName string, w io.Writer) error {
	if err := validateObjectName(objectName); err != nil {
		return err
	}
	obj, err := m.client.GetObject(ctx, m.bucket, objectName, minio.GetObjectOptions{})
	if err != nil {
		return common.Internal("MinIO 文件读取失败", err)
	}
	defer obj.Close()
	if _, err := io.Copy(w, obj); err != nil {
		return common.Internal("MinIO 文件读取失败", err)
	}
	return nil
}

// ObjectSize returns the size of a managed object addressed by URL.
func (m *Minio) ObjectSize(ctx context.Context, fileURL string) (int64, error) {
	name, err := m.objectName(fileURL)
	if err != nil {
		return 0, err
	}
	info, err := m.client.StatObject(ctx, m.bucket, name, minio.StatObjectOptions{})
	if err != nil {
		return 0, common.Internal("MinIO 文件信息读取失败", err)
	}
	return info.Size, nil
}

// ReadRange reads length bytes at offset with a single ranged GET, so verifying a slice of a
// multi-GB object costs one small request instead of a download.
func (m *Minio) ReadRange(ctx context.Context, fileURL string, offset, length int64) ([]byte, error) {
	name, err := m.objectName(fileURL)
	if err != nil {
		return nil, err
	}
	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(offset, offset+length-1); err != nil {
		return nil, common.InvalidArgument("invalid byte range")
	}
	obj, err := m.client.GetObject(ctx, m.bucket, name, opts)
	if err != nil {
		return nil, common.Internal("MinIO 文件读取失败", err)
	}
	defer obj.Close()
	buf, err := io.ReadAll(io.LimitReader(obj, length))
	if err != nil {
		return nil, common.Internal("MinIO 文件读取失败", err)
	}
	return buf, nil
}

// RemoveFile deletes a managed object addressed by URL (no-op for foreign URLs).
func (m *Minio) RemoveFile(ctx context.Context, fileURL string) error {
	if strings.TrimSpace(fileURL) == "" || !m.IsManagedFile(fileURL) {
		return nil
	}
	name, err := m.objectName(fileURL)
	if err != nil {
		return err
	}
	return m.RemoveObject(ctx, name)
}

func (m *Minio) RemoveObject(ctx context.Context, objectName string) error {
	if err := validateObjectName(objectName); err != nil {
		return err
	}
	if err := m.client.RemoveObject(ctx, m.bucket, objectName, minio.RemoveObjectOptions{}); err != nil {
		return common.Internal("MinIO 文件删除失败", err)
	}
	slog.Info("minio_object_deleted", "object", objectName)
	return nil
}

func (m *Minio) IsManagedFile(fileURL string) bool {
	return strings.HasPrefix(fileURL, m.endpoint+"/"+m.bucket+"/")
}

// IsManagedFileWithPrefix checks the object lives under objectPrefix/.
func (m *Minio) IsManagedFileWithPrefix(fileURL, objectPrefix string) bool {
	if !m.IsManagedFile(fileURL) {
		return false
	}
	name, err := m.objectName(fileURL)
	return err == nil && strings.HasPrefix(name, objectPrefix+"/")
}

// ReadableSource returns a 1h presigned GET URL for managed objects, else the source unchanged.
func (m *Minio) ReadableSource(ctx context.Context, source string) (string, error) {
	if !strings.HasPrefix(source, m.endpoint+"/"+m.bucket+"/") {
		return source, nil
	}
	name, err := m.objectName(source)
	if err != nil {
		return "", err
	}
	u, err := m.client.PresignedGetObject(ctx, m.bucket, name, time.Hour, nil)
	if err != nil {
		return "", common.Internal("MinIO 预签名地址生成失败", err)
	}
	return u.String(), nil
}

// PlaybackURL is ReadableSource for the browser: presigned against the public endpoint.
func (m *Minio) PlaybackURL(ctx context.Context, source string) (string, error) {
	if !strings.HasPrefix(source, m.endpoint+"/"+m.bucket+"/") {
		return source, nil
	}
	name, err := m.objectName(source)
	if err != nil {
		return "", err
	}
	u, err := m.public.PresignedGetObject(ctx, m.bucket, name, time.Hour, nil)
	if err != nil {
		return "", common.Internal("MinIO 预签名地址生成失败", err)
	}
	return u.String(), nil
}

func (m *Minio) objectName(fileURL string) (string, error) {
	u, err := url.Parse(fileURL)
	if err != nil {
		return "", common.InvalidArgument("invalid MinIO object URL")
	}
	prefix := "/" + m.bucket + "/"
	start := strings.Index(u.Path, prefix)
	if start < 0 {
		return "", common.InvalidArgument("invalid MinIO object URL")
	}
	name := u.Path[start+len(prefix):]
	if err := validateObjectName(name); err != nil {
		return "", err
	}
	return name, nil
}
