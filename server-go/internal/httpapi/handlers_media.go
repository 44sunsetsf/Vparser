package httpapi

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/upload"
)

func formFile(c *gin.Context, name string) (*multipart.FileHeader, error) {
	fh, err := c.FormFile(name)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, common.InvalidArgument("上传文件超过大小限制")
		}
		return nil, errBadParam // missing part / not multipart
	}
	return fh, nil
}

func (a *API) initUpload(c *gin.Context) {
	filename, err := requiredString(c, "filename")
	if err != nil {
		fail(c, err)
		return
	}
	total, err := requiredInt(c, "totalChunks")
	if err != nil {
		fail(c, err)
		return
	}
	id, err := a.Upload.Initialize(c.Request.Context(), filename, total, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, id)
}

func (a *API) uploadStatus(c *gin.Context) {
	uploadID, err := requiredString(c, "uploadId")
	if err != nil {
		fail(c, err)
		return
	}
	chunks, err := a.Upload.UploadedChunks(c.Request.Context(), uploadID, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, chunks)
}

func (a *API) uploadChunk(c *gin.Context) {
	uploadID, err := requiredString(c, "uploadId")
	if err != nil {
		fail(c, err)
		return
	}
	idx, err := requiredInt(c, "chunkIndex")
	if err != nil {
		fail(c, err)
		return
	}
	total, err := requiredInt(c, "totalChunks")
	if err != nil {
		fail(c, err)
		return
	}
	fh, err := formFile(c, "file")
	if err != nil {
		fail(c, err)
		return
	}
	if err := a.Upload.UploadChunk(c.Request.Context(), uploadID, idx, total, fh, userID(c)); err != nil {
		fail(c, err)
		return
	}
	okVoid(c)
}

func (a *API) completeUpload(c *gin.Context) {
	uploadID, err := requiredString(c, "uploadId")
	if err != nil {
		fail(c, err)
		return
	}
	mf, err := a.Upload.Complete(c.Request.Context(), uploadID, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, model.SummaryOf(mf))
}

func (a *API) uploadFile(c *gin.Context) {
	fh, err := formFile(c, "file")
	if err != nil {
		fail(c, err)
		return
	}
	mf, err := a.Media.IngestFile(c.Request.Context(), fh, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, model.SummaryOf(mf))
}

func (a *API) uploadURL(c *gin.Context) {
	raw, err := requiredString(c, "url")
	if err != nil {
		fail(c, err)
		return
	}
	sourceHeader(c, raw)
	mf, err := a.Media.IngestURL(c.Request.Context(), raw, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, model.SummaryOf(mf))
}

func (a *API) listMedia(c *gin.Context) {
	list, err := a.Media.ListByUser(c.Request.Context(), userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	out := make([]model.MediaSummary, 0, len(list))
	for i := range list {
		out = append(out, model.SummaryOf(&list[i]))
	}
	ok(c, out)
}

func (a *API) playback(c *gin.Context) {
	id, err := requiredInt64(c, "id")
	if err != nil {
		fail(c, err)
		return
	}
	mf, err := a.Media.RequireOwnedMedia(c.Request.Context(), id, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	src, err := a.Media.PlaybackURL(c.Request.Context(), mf.FilePath)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, src)
}

func (a *API) deleteMedia(c *gin.Context) {
	id, err := requiredInt64(c, "id")
	if err != nil {
		fail(c, err)
		return
	}
	if err := a.Media.DeleteOwnedMedia(c.Request.Context(), id, userID(c)); err != nil {
		fail(c, err)
		return
	}
	okVoid(c)
}

// ---- media processing ----

func (a *API) transcribe(c *gin.Context) {
	id, err := requiredInt64(c, "id")
	if err != nil {
		fail(c, err)
		return
	}
	ctx := c.Request.Context()
	if _, err := a.Media.RequireOwnedMedia(ctx, id, userID(c)); err != nil {
		fail(c, err)
		return
	}
	queued, err := a.Transcription.Queue(ctx, id)
	if err != nil {
		fail(c, err)
		return
	}
	if !queued {
		fail(c, common.Business(common.CodeConflict, "文字提取任务正在处理中"))
		return
	}
	if err := a.Transcription.Transcribe(id); err != nil {
		// roll back the "queued" marker so the task does not hang in PROCESSING
		a.Transcription.RejectQueued(ctx, id)
		fail(c, common.Business(common.CodeServiceUnavailable, "任务队列已满，请稍后重试"))
		return
	}
	accepted(c)
}

func (a *API) transcriptionStatus(c *gin.Context) {
	id, err := requiredInt64(c, "id")
	if err != nil {
		fail(c, err)
		return
	}
	mf, err := a.Media.RequireOwnedMedia(c.Request.Context(), id, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	st, err := a.Transcription.Status(c.Request.Context(), mf)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, st)
}

func (a *API) transcriptionEvents(c *gin.Context) {
	id, err := requiredInt64(c, "id")
	if err != nil {
		fail(c, err)
		return
	}
	mf, err := a.Media.RequireOwnedMedia(c.Request.Context(), id, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	st, err := a.Transcription.Status(c.Request.Context(), mf)
	if err != nil {
		fail(c, err)
		return
	}
	sub, err := a.Hub.Subscribe(id, "transcription", "", model.ModeGeneral, st, model.StageTranscription)
	if err != nil {
		fail(c, err)
		return
	}
	a.streamEvents(c, sub)
}

var extRe = regexp.MustCompile(`\.[^.]+$`)

func (a *API) download(c *gin.Context) {
	id, err := requiredInt64(c, "id")
	if err != nil {
		fail(c, err)
		return
	}
	ctx := c.Request.Context()
	mf, err := a.Media.RequireOwnedMedia(ctx, id, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	out, err := a.Media.ExportMP3(ctx, mf)
	if err != nil {
		fail(c, err)
		return
	}
	defer os.Remove(out)
	f, err := os.Open(out)
	if err != nil {
		fail(c, err)
		return
	}
	defer f.Close()
	filename := "audio.mp3"
	if mf.Filename != "" {
		filename = extRe.ReplaceAllString(mf.Filename, "") + ".mp3"
	}
	c.Header("Content-Type", "audio/mpeg")
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+formURLEncode(filename))
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, f)
}

// instantUploadChallenge answers null when no identical object exists (the client falls back to a
// chunked upload) and a byte-range challenge otherwise.
func (a *API) instantUploadChallenge(c *gin.Context) {
	var req upload.ChallengeRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, err)
		return
	}
	ch, err := a.Instant.Challenge(c.Request.Context(), userID(c), req)
	if err != nil {
		fail(c, err)
		return
	}
	if ch == nil {
		ok(c, nil)
		return
	}
	ok(c, ch)
}

func (a *API) instantUpload(c *gin.Context) {
	var req upload.ProofRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, err)
		return
	}
	mf, err := a.Instant.Prove(c.Request.Context(), userID(c), req)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, model.SummaryOf(mf))
}

// sourceHeader echoes the link a visitor asked to import (URL-escaped, at most 300 bytes), so the owner's analytics
// can show what was tried, including imports that failed. It is the visitor's own input; nothing else is revealed.
func sourceHeader(c *gin.Context, raw string) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 300 {
		raw = raw[:300]
	}
	c.Header("X-Vparser-Source", url.QueryEscape(raw))
}
