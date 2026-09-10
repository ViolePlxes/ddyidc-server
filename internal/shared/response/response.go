package response

import (
	"ddyidc/internal/shared/errors"
	"net/http"

	"github.com/zeromicro/go-zero/rest/httpx"
)

// Response 统一响应格式
type Response struct {
	Code int         `json:"code"`
	Msg  string      `json:"msg"`
	Data interface{} `json:"data,omitempty"`
}

// Ok 成功响应
func Ok(w http.ResponseWriter, data interface{}) {
	httpx.OkJson(w, Response{Code: 0, Msg: "success", Data: data})
}

// Fail 失败响应
func Fail(w http.ResponseWriter, err error) {
	if bizErr, ok := err.(*errors.BizError); ok {
		httpx.OkJson(w, Response{Code: bizErr.Code, Msg: bizErr.Msg})
		return
	}
	httpx.OkJson(w, Response{Code: errors.CodeInternal, Msg: err.Error()})
}

// FailWithCode 带错误码的失败响应
func FailWithCode(w http.ResponseWriter, code int, msg string) {
	httpx.OkJson(w, Response{Code: code, Msg: msg})
}
