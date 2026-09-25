package videocontext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dovideo/server/internal/common"
)

// OCR runs the tesseract CLI (`-l chi_sim+eng`, 2 minute timeout).
type OCR struct{ Command string }

func NewOCR(command string) *OCR { return &OCR{Command: command} }

// Args is the tesseract command line after the executable.
func OCRArgs(image string) []string { return []string{image, "stdout", "-l", "chi_sim+eng"} }

func (o *OCR) Recognize(ctx context.Context, image string) (string, error) {
	st, err := os.Stat(image)
	if err != nil || !st.Mode().IsRegular() {
		return "", common.InvalidArgument("OCR image does not exist")
	}
	abs, _ := filepath.Abs(image)
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, o.Command, OCRArgs(abs)...)
	// only stdout is the recognised text; tesseract logs to stderr ("Estimating resolution as 132", "Detected 4
	// diacritics"), which used to end up in the evidence and confuse the agent. stderr is kept for failures only.
	var out, logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &logs
	err = cmd.Run()
	name := filepath.Base(image)
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return "", common.Internal("OCR failed for "+name, common.Internal("OCR execution timed out", nil))
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", common.Internal("OCR failed for "+name,
				common.Internal(fmt.Sprintf("OCR process failed with exit code %d: %s", ee.ExitCode(),
					strings.TrimSpace(logs.String())), nil))
		}
		return "", common.Internal("OCR failed for "+name, err)
	}
	return strings.TrimSpace(out.String()), nil
}
