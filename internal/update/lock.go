package update

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const updateLockKey = "system:update:lock"

type Locker interface {
	Acquire(context.Context, string, time.Duration) (bool, error)
	Release(context.Context, string) error
}

type RedisLocker struct{ client *redis.Client }

func NewRedisLocker(client *redis.Client) *RedisLocker { return &RedisLocker{client: client} }

func (l *RedisLocker) Acquire(ctx context.Context, token string, ttl time.Duration) (bool, error) {
	return l.client.SetNX(ctx, updateLockKey, token, ttl).Result()
}

var releaseScript = redis.NewScript(`if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`)

func (l *RedisLocker) Release(ctx context.Context, token string) error {
	return releaseScript.Run(ctx, l.client, []string{updateLockKey}, token).Err()
}
