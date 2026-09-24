package httpapi

import (
	"errors"
	"reflect"
	"regexp"
	"strings"

	"github.com/go-playground/validator/v10"

	"dovideo/server/internal/common"
)

var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New()
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			return f.Name
		}
		return name
	})
	// @NotBlank: non-null and containing a non-whitespace char
	_ = v.RegisterValidation("notblank", func(fl validator.FieldLevel) bool {
		f := fl.Field()
		if f.Kind() == reflect.Ptr {
			if f.IsNil() {
				return false
			}
			f = f.Elem()
		}
		return f.Kind() == reflect.String && strings.TrimSpace(f.String()) != ""
	})
	return v
}

// form structs carry the group-specific rules of AuthRequest (Register vs Login)
type registerForm struct {
	Username *string `json:"username" validate:"notblank,min=3,max=32"`
	Password *string `json:"password" validate:"notblank,max=128,min=8"`
	Nickname *string `json:"nickname" validate:"omitempty,max=50"`
}

type loginForm struct {
	Username *string `json:"username" validate:"notblank,max=32"`
	Password *string `json:"password" validate:"notblank,max=128"`
}

var messages = map[string]string{
	"registerForm.username.notblank": "账号不能为空",
	"registerForm.username.min":      "账号需为 3-32 位",
	"registerForm.username.max":      "账号需为 3-32 位",
	"registerForm.password.notblank": "密码不能为空",
	"registerForm.password.max":      "密码不能超过 128 位",
	"registerForm.password.min":      "密码需至少 8 位",
	"registerForm.nickname.max":      "昵称不能超过 50 个字符",
	"loginForm.username.notblank":    "账号不能为空",
	"loginForm.username.max":         "账号不能超过 32 位",
	"loginForm.password.notblank":    "密码不能为空",
	"loginForm.password.max":         "密码不能超过 128 位",

	"RouteRequest.goal.notblank": "分析目标不能为空",
	"RouteRequest.goal.max":      "分析目标不能超过 500 字",

	"AgentFeedback.mediaId.required":      "mediaId 不能为空",
	"AgentFeedback.goal.notblank":         "分析目标不能为空",
	"AgentFeedback.goal.max":              "分析目标不能超过 500 字",
	"AgentFeedback.errorType.max":         "错误类型不能超过 64 字",
	"AgentFeedback.comment.max":           "反馈说明不能超过 2000 字",
	"AgentFeedback.correctedGoal.max":     "修正后的分析目标不能超过 500 字",
	"AgentFeedback.correctedTasks.max":    "修正任务最多 5 条",
	"AgentFeedback.correctedTasks[].max":  "每条修正任务不能超过 500 字",
	"AgentFeedback.evidenceTimestamp.gte": "证据时间戳不能为负数",
}

var indexRe = regexp.MustCompile(`\[\d+\]`)

// validateStruct returns a VALIDATION_FAILED error with "field: message" pairs joined by "; ".
func validateStruct(v any) error {
	err := validate.Struct(v)
	if err == nil {
		return nil
	}
	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		return common.InvalidArgument("请求参数不合法")
	}
	parts := make([]string, 0, len(ve))
	for _, fe := range ve {
		ns := indexRe.ReplaceAllString(fe.Namespace(), "[]")
		key := ns + "." + fe.Tag()
		msg, ok := messages[key]
		if !ok {
			msg = "invalid value"
		}
		parts = append(parts, fe.Field()+": "+msg)
	}
	detail := strings.Join(parts, "; ")
	if detail == "" {
		detail = "请求参数校验失败"
	}
	return common.Validation(detail)
}
