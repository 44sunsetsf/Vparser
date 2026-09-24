package httpapi

import (
	"encoding/json"
	"io"

	"github.com/gin-gonic/gin"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
)

// decodeBody reads a JSON request body (unreadable/empty body -> 400).
func decodeBody(c *gin.Context, v any) error {
	dec := json.NewDecoder(c.Request.Body)
	if err := dec.Decode(v); err != nil {
		if err == io.EOF {
			return common.InvalidArgument("请求参数不合法")
		}
		return common.InvalidArgument("请求参数不合法")
	}
	return nil
}

func mapAuthCode(code int) common.ErrorCode {
	switch code {
	case 400:
		return common.CodeInvalidArgument
	case 401:
		return common.CodeUnauthorized
	case 409:
		return common.CodeConflict
	case 429:
		return common.CodeRateLimited
	default:
		return common.CodeInternalError
	}
}

func authResult(c *gin.Context, resp model.AuthResponse) {
	if resp.Code != 200 {
		fail(c, common.Business(mapAuthCode(resp.Code), resp.Msg))
		return
	}
	data := model.AuthData{UserInfo: resp.UserInfo}
	if resp.Token != "" {
		t := resp.Token
		data.Token = &t
	}
	ok(c, data)
}

func (a *API) register(c *gin.Context) {
	var req model.AuthRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, err)
		return
	}
	if err := validateStruct(registerForm(req)); err != nil {
		fail(c, err)
		return
	}
	resp, err := a.Auth.Register(c.Request.Context(), req)
	if err != nil {
		fail(c, err)
		return
	}
	authResult(c, resp)
}

func (a *API) login(c *gin.Context) {
	var req model.AuthRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, err)
		return
	}
	if err := validateStruct(loginForm{Username: req.Username, Password: req.Password}); err != nil {
		fail(c, err)
		return
	}
	resp, err := a.Auth.Login(c.Request.Context(), req)
	if err != nil {
		fail(c, err)
		return
	}
	authResult(c, resp)
}

func (a *API) logout(c *gin.Context) {
	if err := a.Auth.RevokeSession(c.Request.Context(), c.GetHeader("Authorization")); err != nil {
		fail(c, err)
		return
	}
	okVoid(c)
}
