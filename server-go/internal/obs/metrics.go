package obs

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is a private registry (instead of the global default) so tests can build fresh
// handlers and only the metrics below plus Go runtime/process collectors are exposed.
var Registry = prometheus.NewRegistry()

// Latency buckets: HTTP spans sub-millisecond reads to multi-minute uploads; analysis stages
// span seconds to tens of minutes.
var (
	httpBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}
	longBuckets = []float64{.1, .5, 1, 5, 10, 30, 60, 120, 300, 600, 1200, 1800}
)

var (
	HTTPDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_request_duration_seconds", Help: "HTTP request latency by route template.", Buckets: httpBuckets,
	}, []string{"route", "method", "status"})

	AnalysisSubmit = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "analysis_submit_total", Help: "Analysis submissions by result (accepted, duplicate, rate_limited, error).",
	}, []string{"result"})

	KafkaConsumeDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "kafka_consume_duration_seconds", Help: "Time to fully handle one analysis record.", Buckets: longBuckets,
	}, []string{"topic", "outcome"})

	KafkaRetry = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kafka_retry_total", Help: "Records forwarded to a delayed retry topic.",
	}, []string{"topic"})

	KafkaDLQ = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kafka_dlq_total", Help: "Records dead-lettered by reason (permanent, exhausted, poison).",
	}, []string{"reason"})

	RedisLockAcquire = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "redis_lock_acquire_total", Help: "Distributed lock acquisitions by lock family and result.",
	}, []string{"name", "result"})

	VideoContextBuild = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "video_context_build_seconds", Help: "VideoContext build time (ASR + key frames + OCR).", Buckets: longBuckets,
	})

	AgentGRPCDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "agent_grpc_client_duration_seconds", Help: "Agent gRPC call latency by method and status code.", Buckets: longBuckets,
	}, []string{"method", "code"})
)

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		HTTPDuration, AnalysisSubmit, KafkaConsumeDuration, KafkaRetry, KafkaDLQ,
		RedisLockAcquire, VideoContextBuild, AgentGRPCDuration,
	)
}

// Handler serves the registry in the Prometheus text format.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{Registry: Registry})
}
