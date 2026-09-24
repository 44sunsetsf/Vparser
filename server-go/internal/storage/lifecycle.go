package storage

import (
	"context"
	"log/slog"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
)

// ChunkUploadPrefix holds the staged parts of chunked uploads until they are merged.
const ChunkUploadPrefix = "chunk-uploads/"

const (
	chunkRuleID = "expire-abandoned-chunk-uploads"
	// Upload sessions expire after 24h of inactivity, so a part older than this belongs to an
	// abandoned upload. Merged uploads delete their parts right away; this only catches leftovers.
	chunkRetentionDays = 7
)

// withChunkExpiry returns cfg with the chunk-upload expiry rule added or replaced, keeping every
// other rule so operators' own lifecycle settings on the bucket survive a restart.
func withChunkExpiry(cfg *lifecycle.Configuration) *lifecycle.Configuration {
	out := lifecycle.NewConfiguration()
	if cfg != nil {
		for _, r := range cfg.Rules {
			if r.ID != chunkRuleID {
				out.Rules = append(out.Rules, r)
			}
		}
	}
	out.Rules = append(out.Rules, lifecycle.Rule{
		ID:         chunkRuleID,
		Status:     "Enabled",
		RuleFilter: lifecycle.Filter{Prefix: ChunkUploadPrefix},
		Expiration: lifecycle.Expiration{Days: chunkRetentionDays},
	})
	return out
}

// ensureChunkExpiry installs the rule. Failure is logged, not fatal: uploads work without it,
// only abandoned parts would then stay until removed by hand.
func ensureChunkExpiry(ctx context.Context, cli *minio.Client, bucket string) {
	current, err := cli.GetBucketLifecycle(ctx, bucket)
	if err != nil && minio.ToErrorResponse(err).Code != "NoSuchLifecycleConfiguration" {
		slog.Warn("minio_lifecycle_read_failed", "bucket", bucket, "err", err)
		return
	}
	if err := cli.SetBucketLifecycle(ctx, bucket, withChunkExpiry(current)); err != nil {
		slog.Warn("minio_lifecycle_set_failed", "bucket", bucket, "err", err)
		return
	}
	slog.Info("minio_chunk_expiry_ready", "bucket", bucket, "prefix", ChunkUploadPrefix, "days", chunkRetentionDays)
}
