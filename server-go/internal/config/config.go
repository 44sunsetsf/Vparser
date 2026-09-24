// Package config resolves the runtime configuration from environment variables. Secrets have no
// defaults and must be set explicitly (an empty value is allowed, an absent one is not), so a
// misconfigured deployment fails at startup instead of on the first request.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved runtime configuration.
type Config struct {
	ServerPort    string
	ServerAddress string
	CORSOrigins   []string

	DBHost        string
	DBPort        string
	DBName        string
	DBUser        string
	DBPassword    string
	DBTimezone    string
	DBPoolMax     int
	DBPoolMinIdle int

	InteractiveTimeout time.Duration // FollowUp / ClassifyMode / SearchEvidence deadline
	AgentRunTimeout    time.Duration // Run deadline; the agent derives its own budget from it

	APIKey   string // SILICONFLOW_API_KEY (ASR)
	ASRURL   string
	ASRModel string

	RedisHost     string
	RedisPort     string
	RedisDatabase int
	RedisPassword string

	MinioEndpoint       string
	MinioPublicEndpoint string // browser-reachable MinIO base for playback URLs; empty = MinioEndpoint
	MinioAccessKey      string
	MinioSecretKey      string
	MinioBucket         string
	MinioPublicRead     bool

	YtDlpPath  string
	FfmpegDir  string
	OCRCommand string

	KafkaBrokers []string

	AgentGRPCAddr string
	InternalToken string

	OTLPEndpoint string // empty disables trace export
	ServiceName  string
}

func get(env, def string) string {
	if v, ok := os.LookupEnv(env); ok && v != "" {
		return v
	}
	return def
}

func required(env string) (string, error) {
	v, ok := os.LookupEnv(env)
	if !ok {
		return "", fmt.Errorf("missing required environment variable %s", env)
	}
	return v, nil
}

func atoi(env string, def int) int {
	if n, err := strconv.Atoi(get(env, "")); err == nil {
		return n
	}
	return def
}

const defaultAgentMaxDurationMs = 120_000 // same default as the agent service

// Load reads the environment.
func Load() (*Config, error) {
	agentBudgetMs := atoi("AGENT_MAX_DURATION_MS", defaultAgentMaxDurationMs)
	c := &Config{
		ServerPort:          get("SERVER_PORT", "9090"),
		ServerAddress:       get("SERVER_ADDRESS", "127.0.0.1"),
		CORSOrigins:         SplitList(get("CORS_ALLOWED_ORIGINS", "http://localhost:5173,http://127.0.0.1:5173")),
		DBHost:              get("DB_HOST", "localhost"),
		DBPort:              get("DB_PORT", "3307"),
		DBName:              get("DB_NAME", "media_db"),
		DBTimezone:          get("DB_TIMEZONE", "Asia/Shanghai"),
		DBPoolMax:           atoi("DB_POOL_MAX_SIZE", 10),
		DBPoolMinIdle:       atoi("DB_POOL_MIN_IDLE", 2),
		InteractiveTimeout:  time.Duration(atoi("AI_INTERACTIVE_TIMEOUT_MS", 180000)) * time.Millisecond,
		AgentRunTimeout:     time.Duration(atoi("AGENT_RUN_TIMEOUT_MS", agentBudgetMs+60_000)) * time.Millisecond,
		ASRURL:              get("ASR_URL", "https://api.siliconflow.cn/v1/audio/transcriptions"),
		ASRModel:            get("ASR_MODEL", "TeleAI/TeleSpeechASR"),
		RedisHost:           get("REDIS_HOST", "localhost"),
		RedisPort:           get("REDIS_PORT", "6379"),
		RedisDatabase:       atoi("REDIS_DATABASE", 0),
		RedisPassword:       get("REDIS_PASSWORD", ""),
		MinioEndpoint:       get("MINIO_ENDPOINT", "http://localhost:9000"),
		MinioPublicEndpoint: get("MINIO_PUBLIC_ENDPOINT", ""),
		MinioBucket:         get("MINIO_BUCKET", "media"),
		MinioPublicRead:     strings.EqualFold(get("MINIO_PUBLIC_READ", "false"), "true"),
		YtDlpPath:           get("YTDLP_PATH", "yt-dlp"),
		FfmpegDir:           get("FFMPEG_DIR", ""),
		OCRCommand:          get("OCR_COMMAND", "tesseract"),
		KafkaBrokers:        SplitList(get("KAFKA_BROKERS", "localhost:9094")),
		AgentGRPCAddr:       get("AGENT_GRPC_ADDR", "127.0.0.1:9091"),
		OTLPEndpoint:        get("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
		ServiceName:         get("OTEL_SERVICE_NAME", "server-go"),
	}
	var err error
	for _, s := range []struct {
		env string
		dst *string
	}{
		{"DB_USERNAME", &c.DBUser}, {"DB_PASSWORD", &c.DBPassword},
		{"MINIO_ACCESS_KEY", &c.MinioAccessKey}, {"MINIO_SECRET_KEY", &c.MinioSecretKey},
		{"SILICONFLOW_API_KEY", &c.APIKey}, {"INTERNAL_TOKEN", &c.InternalToken},
	} {
		if *s.dst, err = required(s.env); err != nil {
			return nil, err
		}
	}
	if len(c.KafkaBrokers) == 0 {
		return nil, fmt.Errorf("KAFKA_BROKERS must list at least one broker")
	}
	if _, err := time.LoadLocation(c.DBTimezone); err != nil {
		return nil, fmt.Errorf("invalid DB_TIMEZONE %q: %w", c.DBTimezone, err)
	}
	return c, nil
}

// SplitList splits a comma-separated value, trimming blanks and dropping empty entries.
func SplitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// RedisAddr is host:port.
func (c *Config) RedisAddr() string { return c.RedisHost + ":" + c.RedisPort }
