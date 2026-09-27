package notify

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"ddyidc/pkg/cache"
)

const reserveVerificationLua = `
if redis.call('EXISTS', KEYS[1]) == 1 then return 1 end
if tonumber(redis.call('GET', KEYS[2]) or '0') >= tonumber(ARGV[1]) then return 2 end
if tonumber(redis.call('GET', KEYS[3]) or '0') >= 100 then return 3 end
redis.call('SET', KEYS[1], '1', 'EX', 60)
if redis.call('INCR', KEYS[2]) == 1 then redis.call('EXPIRE', KEYS[2], 86400) end
if redis.call('INCR', KEYS[3]) == 1 then redis.call('EXPIRE', KEYS[3], 86400) end
return 0
`

func (s *VerificationService) reserveRedisSend(ctx context.Context, channel, target, scene, ip string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	base := s.key(channel, target, scene)
	ipKey := fmt.Sprintf("notify:send-ip:%x", sha256.Sum256([]byte(ip)))
	return cache.RDB.Eval(ctx, reserveVerificationLua, []string{base + ":cooldown", base + ":daily", ipKey}, maxDailySends).Int()
}

// Verification attempts and successful consumption share one Redis operation.
// Process-local mutexes cannot prevent replay across API instances.
const consumeVerificationLua = `
if redis.call('EXISTS', KEYS[2]) == 1 then return 1 end
local raw = redis.call('GET', KEYS[1])
if not raw then
  if redis.call('EXISTS', KEYS[3]) == 1 then return 2 end
  if redis.call('EXISTS', KEYS[4]) == 1 then return 3 end
  return 4
end
local ok, record = pcall(cjson.decode, raw)
local ttl = redis.call('PTTL', KEYS[1])
if not ok or ttl <= 0 or type(record.code) ~= 'string' then
  redis.call('DEL', KEYS[1]); return 3
end
if record.code ~= ARGV[1] then
  record.attempts = (tonumber(record.attempts) or 0) + 1
  if record.attempts >= tonumber(ARGV[2]) then
    redis.call('DEL', KEYS[1]); redis.call('SET', KEYS[2], '1', 'EX', 900)
  else
    redis.call('SET', KEYS[1], cjson.encode(record), 'PX', ttl)
  end
  return 5
end
redis.call('DEL', KEYS[1], KEYS[4])
redis.call('SET', KEYS[3], '1', 'EX', 600)
return 0
`

func (s *VerificationService) consumeRedisCode(ctx context.Context, channel, target, scene, code string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	base := s.key(channel, target, scene)
	result, err := cache.RDB.Eval(ctx, consumeVerificationLua, []string{base + ":code", base + ":locked", base + ":consumed", base + ":sent"}, strings.TrimSpace(code), maxAttempts).Int()
	if err != nil {
		return errors.New("验证码服务暂时不可用，请稍后重试")
	}
	switch result {
	case 0:
		return nil
	case 1:
		return errors.New("验证失败次数过多，请 15 分钟后重试")
	case 2:
		return errors.New("验证码已使用，请重新获取")
	case 3:
		return errors.New("验证码已过期，请重新获取")
	case 4:
		return errors.New("请先获取验证码")
	default:
		return errors.New("验证码错误")
	}
}
