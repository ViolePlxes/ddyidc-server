package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Config Redis配置
type Config struct {
	Host     string
	Port     int
	Password string
	DB       int
	PoolSize int
}

var RDB *redis.Client

// MustInit 初始化Redis
func MustInit(c Config) {
	RDB = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", c.Host, c.Port),
		Password: c.Password,
		DB:       c.DB,
		PoolSize: c.PoolSize,
	})
	if err := RDB.Ping(context.Background()).Err(); err != nil {
		panic(err)
	}
}

// Get 获取缓存
func Get(ctx context.Context, key string) (string, error) {
	return RDB.Get(ctx, key).Result()
}

// Set 设置缓存
func Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	return RDB.Set(ctx, key, value, expiration).Err()
}

// Del 删除缓存
func Del(ctx context.Context, keys ...string) error {
	return RDB.Del(ctx, keys...).Err()
}
