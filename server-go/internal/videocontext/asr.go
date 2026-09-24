package videocontext

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dovideo/server/internal/common"
)

const asrMaxAttempts = 3

// ASR calls an OpenAI-compatible /audio/transcriptions endpoint (SiliconFlow by default).
type ASR struct {
	APIKey string
	URL    string
	Model  string
	Client *http.Client
	// Sleep is overridable in tests.
	Sleep func(ctx context.Context, d time.Duration) error
}

func NewASR(apiKey, url, model string) *ASR {
	return &ASR{APIKey: apiKey, URL: url, Model: model, Client: &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
			ResponseHeaderTimeout: 3 * time.Minute,
			TLSHandshakeTimeout:   30 * time.Second,
		},
		Timeout: 6 * time.Minute,
	}, Sleep: func(ctx context.Context, d time.Duration) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
}

// retryableError marks 429/5xx/transport failures, the only ones worth retrying.
type retryableError struct {
	msg   string
	cause error
}

func (e *retryableError) Error() string { return e.msg }
func (e *retryableError) Unwrap() error { return e.cause }

// AudioToText transcribes one audio file with up to 3 attempts (1s, 2s back-off).
func (a *ASR) AudioToText(ctx context.Context, filePath string) (string, error) {
	if st, err := os.Stat(filePath); err != nil || !st.Mode().IsRegular() {
		// missing audio is an unexpected state that a rerun regenerates -> retryable
		return "", common.Internal("ASR audio file does not exist", nil)
	}
	var last error
	for attempt := 0; attempt < asrMaxAttempts; attempt++ {
		text, err := a.execute(ctx, filePath)
		if err == nil {
			if strings.TrimSpace(text) == "" {
				return "", common.Internal("ASR 返回空文本", nil)
			}
			return strings.TrimSpace(text), nil
		}
		re, retryable := err.(*retryableError)
		if !retryable {
			return "", err
		}
		last = re
		slog.Warn("asr_attempt_failed", "attempt", attempt+1, "file", filepath.Base(filePath), "err", err)
		if attempt < asrMaxAttempts-1 {
			if serr := a.Sleep(ctx, time.Second<<attempt); serr != nil {
				return "", common.Internal("ASR retry interrupted", serr)
			}
		}
	}
	return "", common.Internal("ASR 调用失败，已达到最大重试次数", last)
}

func (a *ASR) execute(ctx context.Context, filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", common.Internal("ASR audio file does not exist", err)
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", common.Internal("ASR request build failed", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", &retryableError{"ASR audio read failed", err}
	}
	_ = mw.WriteField("model", a.Model)
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL, &buf)
	if err != nil {
		return "", common.Internal("ASR request build failed", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.APIKey)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := a.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", common.Internal("ASR request cancelled", ctx.Err())
		}
		return "", &retryableError{"ASR transport failure", err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", &retryableError{"ASR response read failed", err}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var obj map[string]any
		if err := json.Unmarshal(body, &obj); err != nil {
			return "", common.Internal("ASR response parse failed", err)
		}
		switch v := obj["text"].(type) {
		case nil:
			return "", nil
		case string:
			return v, nil
		default:
			return fmt.Sprint(v), nil
		}
	}
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return "", &retryableError{fmt.Sprintf("ASR transient HTTP %d", resp.StatusCode), nil}
	}
	// 4xx: the request itself is wrong; redelivery cannot help -> permanent
	return "", common.InvalidArgument(fmt.Sprintf("ASR request rejected with HTTP %d", resp.StatusCode))
}
