package errors

import "fmt"

// 业务错误码
const (
	// 通用 1000-1999
	CodeSuccess      = 0
	CodeInvalidParam = 1001
	CodeUnauthorized = 1002
	CodeForbidden    = 1003
	CodeNotFound     = 1004
	CodeInternal     = 1005
	CodeTooManyReq   = 1006

	// 客户域 2000-2999
	CodeUserExists       = 2001
	CodeUserNotExists    = 2002
	CodePasswordWrong    = 2003
	CodePhoneExists      = 2004
	CodeEmailExists      = 2005
	CodeChannelDisabled  = 2017
	CodeOAuthFailed      = 2018
	CodeTempTokenInvalid = 2019
	CodePhoneBound       = 2020

	// 订单域 3000-3999
	CodeOrderNotExists = 3001
	CodeOrderPaid      = 3002
	CodeStockNotEnough = 3003

	// 支付域 4000-4999
	CodePaymentFailed  = 4001
	CodePaymentExpired = 4002
	CodeRefundFailed   = 4003
)

// BizError 业务错误
type BizError struct {
	Code int
	Msg  string
}

func (e *BizError) Error() string {
	return fmt.Sprintf("code=%d, msg=%s", e.Code, e.Msg)
}

func New(code int, msg string) *BizError {
	return &BizError{Code: code, Msg: msg}
}
