package common

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// ErrorCode pairs the business code carried in the response envelope with its HTTP status.
type ErrorCode struct {
	Code   int
	Status int
}

var (
	CodeInvalidArgument    = ErrorCode{40000, http.StatusBadRequest}
	CodeValidationFailed   = ErrorCode{40001, http.StatusBadRequest}
	CodeUnauthorized       = ErrorCode{40100, http.StatusUnauthorized}
	CodeForbidden          = ErrorCode{40300, http.StatusForbidden}
	CodeInviteRequired     = ErrorCode{40301, http.StatusForbidden} // registration needs an invite code
	CodeNotFound           = ErrorCode{40400, http.StatusNotFound}
	CodeConflict           = ErrorCode{40900, http.StatusConflict}
	CodeUnprocessable      = ErrorCode{42200, http.StatusUnprocessableEntity}
	CodeRateLimited        = ErrorCode{42900, http.StatusTooManyRequests}
	CodeQuotaExhausted     = ErrorCode{42901, http.StatusTooManyRequests}     // daily AI spend used up (see billing)
	CodeSourceBlocked      = ErrorCode{42202, http.StatusUnprocessableEntity} // the video site refused the server (login, bot check, region)
	CodeSourceUnsupported  = ErrorCode{42203, http.StatusUnprocessableEntity} // no downloadable video at the link
	CodeSourceTooLarge     = ErrorCode{42204, http.StatusUnprocessableEntity} // over the 2 GB download limit
	CodeSourceTimeout      = ErrorCode{42205, http.StatusUnprocessableEntity} // the site did not answer in time
	CodeInternalError      = ErrorCode{50000, http.StatusInternalServerError}
	CodeServiceUnavailable = ErrorCode{50300, http.StatusServiceUnavailable}
)

// Kind classifies an error by what the caller should do about it. The HTTP layer maps a kind to a
// status code, and the task consumer uses it to choose between retrying, dead-lettering and stopping.
type Kind int

const (
	KindInternal        Kind = iota // unexpected failure; retrying may help
	KindTransient                   // a dependency failed temporarily (network, overload, deadline)
	KindInvalidArgument             // the request can never succeed as sent
	KindForbidden                   // the caller may not touch the resource
	KindNotFound                    // the resource does not exist
	KindBudgetExhausted             // the agent ran out of time/token/cost budget
	KindNotReady                    // the video context has not been built yet
	KindBusiness                    // a user-facing rejection with an explicit ErrorCode
	KindValidation                  // request body failed struct validation
)

// Error is the typed error used across the server. Msg is safe to show to users for the 4xx kinds.
type Error struct {
	Kind  Kind
	Code  ErrorCode // only for KindBusiness
	Msg   string
	Cause error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Cause }

// ReasonCode is a stable, machine-readable name for the kind. It is what gets persisted as
// errorType in the failed-task ledger and the failure checkpoint.
func (e *Error) ReasonCode() string {
	switch e.Kind {
	case KindTransient:
		return "TRANSIENT"
	case KindInvalidArgument:
		return "INVALID_ARGUMENT"
	case KindForbidden:
		return "FORBIDDEN"
	case KindNotFound:
		return "NOT_FOUND"
	case KindBudgetExhausted:
		return "BUDGET_EXHAUSTED"
	case KindNotReady:
		return "NOT_READY"
	case KindBusiness:
		return "REJECTED"
	case KindValidation:
		return "VALIDATION_FAILED"
	default:
		return "INTERNAL"
	}
}

func InvalidArgument(msg string) *Error { return &Error{Kind: KindInvalidArgument, Msg: msg} }
func Forbidden(msg string) *Error       { return &Error{Kind: KindForbidden, Msg: msg} }
func NotFound(msg string) *Error        { return &Error{Kind: KindNotFound, Msg: msg} }
func Internal(msg string, cause error) *Error {
	return &Error{Kind: KindInternal, Msg: msg, Cause: cause}
}
func Transient(msg string, cause error) *Error {
	return &Error{Kind: KindTransient, Msg: msg, Cause: cause}
}
func BudgetExhausted(msg string, cause error) *Error {
	return &Error{Kind: KindBudgetExhausted, Msg: msg, Cause: cause}
}
func Business(code ErrorCode, msg string) *Error {
	return &Error{Kind: KindBusiness, Code: code, Msg: msg}
}
func Validation(detail string) *Error { return &Error{Kind: KindValidation, Msg: detail} }

// NotReadyMessage is shown when an interactive call needs a video context that does not exist yet.
const NotReadyMessage = "视频内容尚未解析完成，请先完成一次 Video Agent 分析"

func NotReady() *Error { return &Error{Kind: KindNotReady, Msg: NotReadyMessage} }

// Has reports whether any error in the chain has the given kind.
func Has(err error, kind Kind) bool {
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		if je, ok := cur.(*Error); ok && je.Kind == kind {
			return true
		}
	}
	return false
}

// CodeOf returns the reason code of the most specific (innermost) typed error in the chain, so a
// transient gRPC failure wrapped as "analysis failed" is still recorded as TRANSIENT.
func CodeOf(err error) string {
	if err == nil {
		return "UNKNOWN"
	}
	code := ""
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		if je, ok := cur.(*Error); ok {
			code = je.ReasonCode()
		}
	}
	switch {
	case code != "" && code != "INTERNAL":
		return code
	case errors.Is(err, context.DeadlineExceeded):
		return "DEADLINE_EXCEEDED"
	case errors.Is(err, context.Canceled):
		return "CANCELED"
	case code != "":
		return code
	}
	return "INTERNAL"
}

// RootCause walks Unwrap to the innermost error.
func RootCause(err error) error {
	cur := err
	for {
		next := errors.Unwrap(cur)
		if next == nil || next == cur {
			return cur
		}
		cur = next
	}
}

// ToHTTP maps an error to the HTTP status and response envelope. Internal and transient details
// never leak to the client.
func ToHTTP(err error) (int, Result) {
	var je *Error
	if errors.As(err, &je) {
		switch je.Kind {
		case KindBusiness:
			return je.Code.Status, Fail(je.Code, safe(je.Msg, "请求处理失败"))
		case KindValidation:
			return CodeValidationFailed.Status, Fail(CodeValidationFailed, safe(je.Msg, "请求参数校验失败"))
		case KindInvalidArgument:
			return CodeInvalidArgument.Status, Fail(CodeInvalidArgument, safe(je.Msg, "请求参数不合法"))
		case KindNotFound:
			return CodeNotFound.Status, Fail(CodeNotFound, safe(je.Msg, "资源不存在"))
		case KindForbidden:
			return CodeForbidden.Status, Fail(CodeForbidden, safe(je.Msg, "无访问权限"))
		case KindBudgetExhausted:
			return CodeUnprocessable.Status, Fail(CodeUnprocessable, safe(je.Msg, "任务已超出预算"))
		case KindNotReady:
			return CodeConflict.Status, Fail(CodeConflict, safe(je.Msg, "视频上下文尚未就绪"))
		}
	}
	return CodeInternalError.Status, Fail(CodeInternalError, "服务暂时不可用")
}

func safe(msg, fallback string) string {
	if strings.TrimSpace(msg) == "" {
		return fallback
	}
	return msg
}

// maxCauseDepth bounds the chain walk so a pathological (or cyclic) wrapper cannot spin forever.
const maxCauseDepth = 16

// IsPermanentFailure reports whether redelivering the task would fail the same way: some cause in
// the chain is an invalid-argument, forbidden or not-found error.
func IsPermanentFailure(err error) bool {
	cur := err
	for depth := 0; cur != nil && depth < maxCauseDepth; depth++ {
		if je, ok := cur.(*Error); ok {
			switch je.Kind {
			case KindInvalidArgument, KindForbidden, KindNotFound:
				return true
			}
		}
		next := errors.Unwrap(cur)
		if next == cur {
			break
		}
		cur = next
	}
	return false
}

// Describe joins the messages of the whole chain ("analysis failed: agent unavailable: dial tcp
// ..."). *Error.Error() deliberately returns only its own message (it may be shown to users), so
// logs and the dead-letter triage header use this instead.
func Describe(err error) string {
	var parts []string
	for depth, cur := 0, err; cur != nil && depth < maxCauseDepth; depth, cur = depth+1, errors.Unwrap(cur) {
		msg := cur.Error()
		if je, ok := cur.(*Error); ok {
			msg = je.Msg
		}
		if msg != "" && (len(parts) == 0 || !strings.HasSuffix(parts[len(parts)-1], msg)) {
			parts = append(parts, msg)
		}
		if _, ok := cur.(*Error); !ok {
			break // a plain error's message already includes its wrapped causes
		}
	}
	return strings.Join(parts, ": ")
}
