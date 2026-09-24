// Package taskkeys derives goal digests and the Redis keys shared with the agent service.
package taskkeys

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
)

var md5Pattern = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)

// unitSeparator is U+241F, keeping "mode+goal" from colliding with a real goal.
const unitSeparator = "␟"

// NormalizeContentHash returns the lower-cased md5 or "media-<id>" as fallback.
func NormalizeContentHash(mediaID int64, contentHash string) string {
	if md5Pattern.MatchString(contentHash) {
		return strings.ToLower(contentHash)
	}
	return "media-" + strconv.FormatInt(mediaID, 10)
}

// trimControl strips code points <= U+0020 at both ends. Unicode spaces such as U+3000 are kept on
// purpose: the digest must be byte-identical across services, and "ASCII control or space" is the
// one trim rule every language implements the same way.
func trimControl(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r <= 0x20 })
}

// isBlank reports whether s has only whitespace.
func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// GoalDigest is sha256(trim(goal)) for GENERAL (and empty) mode, sha256(MODE + U+241F + trim(goal))
// otherwise. GENERAL keeps the bare form so its keys stay stable if modes are added. A blank goal is
// an invalid argument.
func GoalDigest(goal string, mode model.AnalysisMode) (string, error) {
	if isBlank(goal) {
		return "", common.InvalidArgument("analysis goal is required")
	}
	if mode == "" || mode == model.ModeGeneral {
		return sha256Hex(trimControl(goal)), nil
	}
	return sha256Hex(string(mode) + unitSeparator + trimControl(goal)), nil
}

func sha256Hex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func Active(contentHash, digest string) string {
	return "analysis:active:" + contentHash + ":" + digest
}
func Lock(contentHash, digest string) string { return "lock:analysis:" + contentHash + ":" + digest }
func Completed(scope, digest string) string  { return "analysis:completed:" + scope + ":" + digest }
func ContextOwner(contentHash string) string { return "analysis:context-owner:" + contentHash }
func ContextLock(contentHash string) string  { return "lock:analysis-context:" + contentHash }
