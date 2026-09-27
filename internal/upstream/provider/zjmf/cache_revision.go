package zjmf

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"ddyidc/pkg/cache"
	"github.com/redis/go-redis/v9"
)

// Generations, rather than delete messages, also invalidate disconnected nodes.
// A missing/evicted Redis key gets a fresh nonce, never a reused generation zero.
func (p *Provider) productGeneration(id int, replace bool) (string, error) {
	if cache.RDB == nil {
		// Standalone provider tests have no Redis. Do not cache in this mode.
		return generationNonce()
	}
	parent := p.instanceContext
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	key := "upstream:product-generation:" + p.tokenKey() + ":" + strconv.Itoa(id)
	if !replace {
		v, err := cache.RDB.Get(ctx, key).Result()
		if err == nil && v != "" {
			return v, nil
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			return "", errors.New("product cache coordination unavailable")
		}
	}
	nonce, err := generationNonce()
	if err != nil {
		return "", err
	}
	if replace {
		err = cache.RDB.Set(ctx, key, nonce, 24*time.Hour).Err()
	} else {
		err = cache.RDB.SetNX(ctx, key, nonce, 24*time.Hour).Err()
	}
	if err != nil {
		return "", errors.New("product cache coordination unavailable")
	}
	v, err := cache.RDB.Get(ctx, key).Result()
	if err != nil || v == "" {
		return "", errors.New("product cache coordination unavailable")
	}
	return v, nil
}

func generationNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (p *Provider) productCacheKey(id int, kind string) (string, error) {
	generation, err := p.productGeneration(id, false)
	if err != nil {
		return "", err
	}
	return p.tokenKey() + ":" + kind + ":" + strconv.Itoa(id) + ":" + generation, nil
}
