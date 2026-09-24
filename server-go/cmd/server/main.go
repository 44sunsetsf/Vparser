// Command server is the DoVideoAI gateway: HTTP API, uploads, task scheduling over Kafka, video
// context building, and the gRPC client of the agent service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"
	"go.opentelemetry.io/otel"
	_ "time/tzdata" // DB_TIMEZONE lookups work in minimal images

	"dovideo/server/internal/agentclient"
	"dovideo/server/internal/analysis"
	"dovideo/server/internal/auth"
	"dovideo/server/internal/checkpoint"
	"dovideo/server/internal/config"
	"dovideo/server/internal/httpapi"
	"dovideo/server/internal/media"
	"dovideo/server/internal/mq"
	"dovideo/server/internal/obs"
	"dovideo/server/internal/ratelimit"
	"dovideo/server/internal/redislock"
	"dovideo/server/internal/repo"
	"dovideo/server/internal/storage"
	"dovideo/server/internal/taskevents"
	"dovideo/server/internal/upload"
	"dovideo/server/internal/videocontext"
	"dovideo/server/internal/workerpool"
	"dovideo/server/internal/ytdlp"
)

const (
	shutdownTimeout = 30 * time.Second
	topicsMaxWait   = 90 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("server_failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := obs.SetupTracing(ctx, cfg.OTLPEndpoint, cfg.ServiceName)
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(sctx)
	}()

	// schema first, then the pool
	if err := repo.Migrate(cfg); err != nil {
		return err
	}
	db, err := repo.Open(cfg)
	if err != nil {
		return err
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()

	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddr(), Password: cfg.RedisPassword, DB: cfg.RedisDatabase,
		DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
	})
	defer rdb.Close()
	if err := redisotel.InstrumentTracing(rdb); err != nil {
		return err
	}

	store, err := storage.New(ctx, cfg)
	if err != nil {
		return err
	}

	// Bounded pools: AI calls are I/O bound, ASR is network bound, OCR is CPU bound.
	ocrWorkers := min(8, max(1, runtime.NumCPU()/2))
	aiPool := workerpool.New("ai", 8, 100)
	asrPool := workerpool.New("asr", 8, 50)
	ocrPool := workerpool.New("ocr", ocrWorkers, 20)

	agent, err := agentclient.Dial(cfg.AgentGRPCAddr, cfg.InternalToken,
		agentclient.Timeouts{Run: cfg.AgentRunTimeout, Interactive: cfg.InteractiveTimeout})
	if err != nil {
		return err
	}
	defer agent.Close()

	users := repo.NewUsers(db)
	mediaRepo := repo.NewMedia(db)
	cp := checkpoint.New(rdb, repo.NewCheckpoints(db))
	hub := taskevents.New(rdb)
	locker := redislock.New(rdb)
	limiter := ratelimit.New(rdb)

	transcriber := &videocontext.Transcriber{ASR: videocontext.NewASR(cfg.APIKey, cfg.ASRURL, cfg.ASRModel)}
	vctx := &videocontext.Service{
		Transcriber: transcriber, OCR: videocontext.NewOCR(cfg.OCRCommand), Store: store,
		ASRPool: asrPool, OCRPool: ocrPool,
	}
	dl := ytdlp.New(cfg.YtDlpPath, cfg.FfmpegDir)
	mediaSvc := media.NewService(mediaRepo, rdb, locker, store, cp, agent, vctx, dl)
	authSvc := auth.New(rdb, users)
	uploadSvc := upload.New(rdb, locker, store, mediaSvc)
	instant := upload.NewInstant(rdb, locker, store, mediaSvc)

	// Kafka: one tracer shared by producer and consumer so request -> record -> worker -> gRPC is
	// a single trace (traceparent travels in record headers).
	kTracer := kotel.NewTracer(kotel.TracerProvider(otel.GetTracerProvider()),
		kotel.TracerPropagator(otel.GetTextMapPropagator()), kotel.ConsumerGroup(mq.ConsumerGroup))
	kHooks := kotel.NewKotel(kotel.WithTracer(kTracer)).Hooks()
	if err := ensureTopics(ctx, cfg.KafkaBrokers); err != nil {
		return err
	}
	producer, err := mq.NewProducer(cfg.KafkaBrokers, kHooks...)
	if err != nil {
		return err
	}
	defer producer.Close()

	dispatcher := &analysis.Dispatcher{Media: mediaSvc, Rdb: rdb, Sender: producer, Limiter: limiter, Hub: hub, Agent: agent}
	status := &analysis.StatusService{CP: cp, Agent: agent, Dispatcher: dispatcher}
	failed := &analysis.FailedTasks{Repo: repo.NewFailedTasks(db), Sender: producer, Rdb: rdb, Hub: hub}
	transcriptions := &analysis.Transcriptions{MediaRepo: mediaRepo, Media: mediaSvc, Transcriber: transcriber,
		Rdb: rdb, Hub: hub, Pool: aiPool}
	analyzer := &analysis.Analyzer{MediaRepo: mediaRepo, Media: mediaSvc, CP: cp, Hub: hub, Rdb: rdb,
		Locker: locker, Builder: vctx, Agent: agent}

	go hub.Run(ctx, rdb)

	consumer := &mq.Consumer{Rdb: rdb, Locker: locker, Analyzer: analyzer, Agent: agent, Stages: cp,
		Publisher: producer, Ledger: failed, Media: mediaSvc, Events: hub}
	runner, err := mq.NewRunner(cfg.KafkaBrokers, consumer, kTracer, kHooks...)
	if err != nil {
		return err
	}
	pollCtx, stopPolling := context.WithCancel(context.Background())
	defer stopPolling()
	pollDone := make(chan struct{})
	go func() { runner.Run(pollCtx); close(pollDone) }()

	shutdownCh := make(chan struct{})
	api := &httpapi.API{
		Auth: authSvc, Media: mediaSvc, Upload: uploadSvc, Instant: instant, Dispatcher: dispatcher, Status: status,
		FailedTasks: failed, Transcription: transcriptions, Hub: hub, CP: cp, Agent: agent,
		Limiter: limiter, Rdb: rdb, AIPool: aiPool, Users: users,
		CORSOrigins: cfg.CORSOrigins, InteractiveTimeout: cfg.InteractiveTimeout, ShutdownCh: shutdownCh,
	}
	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.ServerAddress, cfg.ServerPort),
		Handler:           api.Router(),
		ReadHeaderTimeout: 10 * time.Second, // no body/write timeouts: 2GB uploads and 30 min SSE streams
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("server_listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// Graceful shutdown within 30s: end SSE streams and drain HTTP, stop fetching, let in-flight
	// tasks finish and commit, leave the consumer group, then drain the worker pools.
	slog.Info("server_shutting_down")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	close(shutdownCh)
	if err := srv.Shutdown(sctx); err != nil {
		slog.Warn("http_shutdown_incomplete", "err", err)
	}
	stopPolling()
	<-pollDone
	runner.Shutdown(sctx)
	for _, p := range []*workerpool.Pool{aiPool, asrPool, ocrPool} {
		p.Shutdown(sctx)
	}
	return nil
}

func ensureTopics(ctx context.Context, brokers []string) error {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return err
	}
	defer cl.Close()
	return mq.EnsureTopics(ctx, kadm.NewClient(cl), mq.Topics, topicsMaxWait)
}
