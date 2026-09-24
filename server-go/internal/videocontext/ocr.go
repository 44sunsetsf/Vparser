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
	// stderr is merged into the captured text so warnings stay next to the output they concern
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	name := filepath.Base(image)
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return "", common.Internal("OCR failed for "+name, common.Internal("OCR execution timed out", nil))
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", common.Internal("OCR failed for "+name,
				common.Internal(fmt.Sprintf("OCR process failed with exit code %d", ee.ExitCode()), nil))
		}
		return "", common.Internal("OCR failed for "+name, err)
	}
	return strings.TrimSpace(out.String()), nil
}
