package mq

import (
	"encoding/json"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/twmb/franz-go/pkg/kgo"

	"dovideo/server/internal/model"
)

// Record headers (contract §3). traceparent is injected and extracted by the kotel hooks.
const (
	HeaderAttempt    = "x-attempt"     // 1-based delivery attempt of the task
	HeaderNotBefore  = "x-not-before"  // unix millis; a retry record is not processed earlier
	HeaderFirstError = "x-first-error" // the error of attempt 1, kept for the dead-letter triage
	HeaderDLQReason  = "x-dlq-reason"  // permanent | exhausted | poison
)

const maxFirstErrorBytes = 512

// Delivery is a decoded record.
type Delivery struct {
	Topic      string
	Partition  int32
	Offset     int64
	Key        []byte
	Value      []byte
	Msg        *model.AnalysisTaskMsg // nil when the value is not valid JSON
	DecodeErr  error
	Attempt    int
	NotBefore  time.Time // zero when absent
	FirstError string
}

func header(rec *kgo.Record, key string) (string, bool) {
	for _, h := range rec.Headers {
		if h.Key == key {
			return string(h.Value), true
		}
	}
	return "", false
}

// DecodeRecord parses the value and the task headers. Missing or malformed headers fall back to
// "first attempt, due now" so a record produced by an older or foreign producer is still handled.
func DecodeRecord(rec *kgo.Record) Delivery {
	d := Delivery{Topic: rec.Topic, Partition: rec.Partition, Offset: rec.Offset, Key: rec.Key, Value: rec.Value, Attempt: 1}
	if err := json.Unmarshal(rec.Value, &d.Msg); err != nil {
		d.Msg, d.DecodeErr = nil, err
	}
	if v, ok := header(rec, HeaderAttempt); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			d.Attempt = n
		}
	}
	if v, ok := header(rec, HeaderNotBefore); ok {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms > 0 {
			d.NotBefore = time.UnixMilli(ms)
		}
	}
	d.FirstError, _ = header(rec, HeaderFirstError)
	return d
}

// taskHeaders builds the header set of a forwarded record. Only the x-* task headers are copied;
// traceparent is re-injected from the current span so the retry hop stays in the same trace.
func taskHeaders(attempt int, notBefore time.Time, firstError string, extra ...kgo.RecordHeader) []kgo.RecordHeader {
	hs := []kgo.RecordHeader{{Key: HeaderAttempt, Value: []byte(strconv.Itoa(attempt))}}
	if !notBefore.IsZero() {
		hs = append(hs, kgo.RecordHeader{Key: HeaderNotBefore, Value: []byte(strconv.FormatInt(notBefore.UnixMilli(), 10))})
	}
	if firstError != "" {
		hs = append(hs, kgo.RecordHeader{Key: HeaderFirstError, Value: []byte(truncateBytes(firstError, maxFirstErrorBytes))})
	}
	return append(hs, extra...)
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
