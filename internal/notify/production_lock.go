package notify

import (
	"context"
	"crypto/sha256"
	"ddyidc/pkg/cache"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func productionLockScene(scope string) string {
	return fmt.Sprintf("productionLock:%x", sha256.Sum256([]byte(scope)))
}

// Only an authenticated, ownership-checked instance handler may supply scope.
// Public send/verify endpoints cannot select this scene or consume these codes.
func (s *VerificationService) SendProductionLockCode(w http.ResponseWriter, r *http.Request, email, scope string) {
	if cache.RDB == nil {
		respond(w, 503, 503, nil, "验证码服务暂时不可用，请稍后重试")
		return
	}
	if !validEmail(email) || scope == "" {
		respond(w, 400, 400, nil, "邮箱或验证请求无效")
		return
	}
	s.sendScoped(w, r, ChannelEmail, strings.ToLower(email), "productionLock", productionLockScene(scope))
}
func (s *VerificationService) ConsumeProductionLockCode(ctx context.Context, email, scope, code string) error {
	if cache.RDB == nil {
		return errors.New("验证码服务暂时不可用，请稍后重试")
	}
	if len(code) != 6 || !allDigits(code) || !validEmail(email) || scope == "" {
		return errors.New("请输入六位邮箱验证码")
	}
	if err := s.verificationChannelReady(ctx, ChannelEmail, "productionLock"); err != nil {
		return err
	}
	return s.consumeRedisCode(ctx, ChannelEmail, strings.ToLower(email), productionLockScene(scope), code)
}
