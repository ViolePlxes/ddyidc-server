package ksyun

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Use the context-aware SDK API. The generated Send helpers replace caller
// contexts with context.Background and may leak raw upstream response bodies.
func callSDK[T any, R any](ctx context.Context, send func(context.Context, R) (int, string, error), request R) (*T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	code, body, err := send(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("金山云请求失败，请核对网络及任务记录")
	}
	if code < 200 || code >= 300 {
		if reason := safeCloudErrorCode(body); reason != "" {
			if (code == 400 || code == 404) && (reason == "DomainNotFound" || reason == "InvalidDomain.NotFound") {
				return nil, provider.ErrDomainNotFound
			}
			if reason == "UnOpenKs3Service" {
				return nil, errors.New("日志服务配置后可用：尚未开通日志存储服务")
			}
			return nil, fmt.Errorf("金山云请求未通过（HTTP %d，%s）", code, reason)
		}
		return nil, fmt.Errorf("金山云请求未通过（HTTP %d）", code)
	}
	if body == "" {
		if code == 200 && emptySuccessSDKResponse[T]() {
			var result T
			return &result, nil
		}
		return nil, errors.New("金山云返回空响应，结果待核查")
	}
	var envelope struct {
		Error  json.RawMessage `json:"Error"`
		Errors json.RawMessage `json:"Errors"`
	}
	if err = json.Unmarshal([]byte(body), &envelope); err != nil {
		return nil, errors.New("金山云响应格式无效")
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" || len(envelope.Errors) > 0 && string(envelope.Errors) != "null" && string(envelope.Errors) != "[]" {
		return nil, errors.New("金山云拒绝请求，请核对接口权限及参数")
	}
	var result T
	if err = json.Unmarshal([]byte(body), &result); err != nil {
		if compatible, e := sdkCompatibleJSON[T]([]byte(body)); e == nil {
			var normalized T
			if e = json.Unmarshal(compatible, &normalized); e == nil {
				return &normalized, nil
			}
		}
		var mismatch *json.UnmarshalTypeError
		if errors.As(err, &mismatch) {
			return nil, fmt.Errorf("金山云响应字段 %s 类型不符（%s → %s）", mismatch.Field, mismatch.Value, mismatch.Type.String())
		}
		return nil, errors.New("金山云响应无法解析")
	}
	return &result, nil
}

// Official configuration/enable/delete APIs may acknowledge with HTTP 200 and
// no body. Only the SDK responses with no payload fields may use this contract.
func emptySuccessSDKResponse[T any]() bool {
	typ := reflect.TypeOf((*T)(nil)).Elem()
	if typ.PkgPath() != "github.com/kingsoftcloud/sdk-go/v2/ksyun/client/cdn/v3" || typ.Kind() != reflect.Struct || (typ.NumField() != 1 && typ.NumField() != 2) {
		return false
	}
	name := typ.Name()
	allowed := strings.HasPrefix(name, "Set") || strings.HasPrefix(name, "Modify") || strings.HasPrefix(name, "Delete") || strings.HasPrefix(name, "StartStop")
	if !allowed || !typ.Field(0).Anonymous || typ.Field(0).Name != "BaseResponse" {
		return false
	}
	if typ.NumField() == 1 {
		return true
	}
	field := typ.Field(1)
	return (field.Name == name || (name == "SetTLSVersionConfigResponse" && field.Name == "TLSVersionConfigResponse")) && field.Type == reflect.TypeOf((*string)(nil))
}

// Only documented codes may enter logs; never expose vendor Message or body.
func safeCloudErrorCode(body string) string {
	var value struct {
		Code  string
		Error struct{ Code string }
	}
	if json.Unmarshal([]byte(body), &value) != nil {
		return ""
	}
	code := value.Error.Code
	if code == "" {
		code = value.Code
	}
	switch code {
	case "UnOpenKs3Service", "DomainNotFound", "InvalidActionType", "InvalidDomainStatus", "StatusCannotOrNoneedModified", "InvalidInterval", "InvalidCdnType", "InvalidArea", "InvalidParameterValue", "MissingParameter", "InvalidAction", "InvalidVersion", "BindException", "InvalidDomain.NotFound", "InvalidDomain.Offline", "IllegalOperation", "NoAvailableDomain", "DomainCdnTypeNotMatch", "InvalidStartTime.Malformed", "InvalidEndTime.Malformed", "InvalidGranularity", "AccessDenied", "SignatureDoesNotMatch", "InvalidAccessKeyId":
		return code
	}
	return ""
}
