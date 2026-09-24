package httpapi

import (
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/gin-gonic/gin"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
)

// rawParam reads a request parameter from the query string, then the form body.
func rawParam(c *gin.Context, name string) (string, bool) {
	if v, ok := c.GetQuery(name); ok {
		return v, true
	}
	return c.GetPostForm(name)
}

var errBadParam = common.InvalidArgument("请求参数不合法")

// requiredString is @RequestParam String (missing -> 400).
func requiredString(c *gin.Context, name string) (string, error) {
	v, ok := rawParam(c, name)
	if !ok {
		return "", errBadParam
	}
	return v, nil
}

// optionalString is @RequestParam(required=false) String.
func optionalString(c *gin.Context, name string) (string, bool) { return rawParam(c, name) }

// stringWithDefault is @RequestParam(defaultValue=...): empty/missing use the default.
func stringWithDefault(c *gin.Context, name, def string) string {
	if v, ok := rawParam(c, name); ok && v != "" {
		return v
	}
	return def
}

// requiredInt64 is @RequestParam Long.
func requiredInt64(c *gin.Context, name string) (int64, error) {
	v, ok := rawParam(c, name)
	if !ok || strings.TrimSpace(v) == "" {
		return 0, errBadParam
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0, errBadParam
	}
	return n, nil
}

// requiredInt is @RequestParam int.
func requiredInt(c *gin.Context, name string) (int, error) {
	v, ok := rawParam(c, name)
	if !ok || strings.TrimSpace(v) == "" {
		return 0, errBadParam
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
	if err != nil {
		return 0, errBadParam
	}
	return int(n), nil
}

const maxGoalLength = 500

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// normalizeText trims user text and bounds it at 500 UTF-16 code units (the unit the browser's
// maxlength counts in, so client and server agree on the limit).
func normalizeText(value, field string) (string, error) {
	if strings.TrimSpace(value) == "" || utf16Len(value) > maxGoalLength {
		return "", common.InvalidArgument(field + "不能为空且不能超过 500 字")
	}
	return strings.TrimSpace(value), nil
}

func modeParam(c *gin.Context) (model.AnalysisMode, error) {
	v, _ := optionalString(c, "mode")
	return model.ModeFromRequest(v)
}

// formURLEncode percent-encodes a filename for Content-Disposition filename*= (unreserved bytes are
// kept, space becomes +, everything else is %XX of its UTF-8 bytes).
func formURLEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for _, by := range []byte(s) {
		switch {
		case by >= 'a' && by <= 'z', by >= 'A' && by <= 'Z', by >= '0' && by <= '9',
			by == '.', by == '-', by == '*', by == '_':
			b.WriteByte(by)
		case by == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hex[by>>4])
			b.WriteByte(hex[by&15])
		}
	}
	return b.String()
}

func parsePathID(v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, errBadParam
	}
	return n, nil
}
