package storage

import (
	"testing"

	"github.com/minio/minio-go/v7/pkg/lifecycle"
)

func TestWithChunkExpiryAddsRule(t *testing.T) {
	cfg := withChunkExpiry(nil)
	if len(cfg.Rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(cfg.Rules))
	}
	r := cfg.Rules[0]
	if r.RuleFilter.Prefix != ChunkUploadPrefix || r.Status != "Enabled" || int(r.Expiration.Days) != chunkRetentionDays {
		t.Fatalf("unexpected rule: %+v", r)
	}
}

func TestWithChunkExpiryKeepsOtherRulesAndIsIdempotent(t *testing.T) {
	existing := lifecycle.NewConfiguration()
	existing.Rules = []lifecycle.Rule{
		{ID: "operator-rule", Status: "Enabled", RuleFilter: lifecycle.Filter{Prefix: "tmp/"}, Expiration: lifecycle.Expiration{Days: 1}},
		{ID: chunkRuleID, Status: "Enabled", RuleFilter: lifecycle.Filter{Prefix: ChunkUploadPrefix}, Expiration: lifecycle.Expiration{Days: 30}},
	}
	cfg := withChunkExpiry(withChunkExpiry(existing))
	if len(cfg.Rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(cfg.Rules))
	}
	if cfg.Rules[0].ID != "operator-rule" {
		t.Fatalf("operator rule lost: %+v", cfg.Rules)
	}
	if int(cfg.Rules[1].Expiration.Days) != chunkRetentionDays {
		t.Fatalf("chunk rule not replaced: %+v", cfg.Rules[1])
	}
}
