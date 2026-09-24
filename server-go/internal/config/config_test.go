package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func setSecrets(t *testing.T) {
	t.Helper()
	for _, k := range []string{"DB_USERNAME", "DB_PASSWORD", "MINIO_ACCESS_KEY", "MINIO_SECRET_KEY",
		"SILICONFLOW_API_KEY", "INTERNAL_TOKEN"} {
		t.Setenv(k, "x")
	}
}

func TestLoadDefaults(t *testing.T) {
	setSecrets(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DBHost != "localhost" || c.DBPort != "3307" || c.DBName != "media_db" || c.DBTimezone != "Asia/Shanghai" {
		t.Fatalf("db defaults: %+v", c)
	}
	if strings.Join(c.KafkaBrokers, ",") != "localhost:9094" || c.AgentGRPCAddr != "127.0.0.1:9091" {
		t.Fatalf("kafka/agent defaults: %v %s", c.KafkaBrokers, c.AgentGRPCAddr)
	}
	if c.AgentRunTimeout != 180*time.Second || c.ServiceName != "server-go" || c.OTLPEndpoint != "" {
		t.Fatalf("run timeout/otel: %v %s %q", c.AgentRunTimeout, c.ServiceName, c.OTLPEndpoint)
	}
}

func TestLoadOverrides(t *testing.T) {
	setSecrets(t)
	t.Setenv("KAFKA_BROKERS", "k1:9092, k2:9092,")
	t.Setenv("AGENT_MAX_DURATION_MS", "1000")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.KafkaBrokers) != 2 || c.KafkaBrokers[1] != "k2:9092" {
		t.Fatalf("brokers %v", c.KafkaBrokers)
	}
	if c.AgentRunTimeout != 61*time.Second {
		t.Fatalf("run timeout follows the agent budget: %v", c.AgentRunTimeout)
	}
	t.Setenv("AGENT_RUN_TIMEOUT_MS", "5000")
	if c, _ = Load(); c.AgentRunTimeout != 5*time.Second {
		t.Fatalf("explicit run timeout: %v", c.AgentRunTimeout)
	}
	t.Setenv("DB_TIMEZONE", "Mars/Olympus")
	if _, err := Load(); err == nil {
		t.Fatal("invalid timezone must fail fast")
	}
}

func TestLoadRequiresSecrets(t *testing.T) {
	t.Setenv("DB_USERNAME", "")
	_ = os.Unsetenv("DB_USERNAME")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DB_USERNAME") {
		t.Fatalf("expected missing secret error, got %v", err)
	}
}

func TestSplitList(t *testing.T) {
	got := SplitList(" http://a , ,http://b ")
	if len(got) != 2 || got[0] != "http://a" || got[1] != "http://b" {
		t.Fatalf("got %v", got)
	}
}
