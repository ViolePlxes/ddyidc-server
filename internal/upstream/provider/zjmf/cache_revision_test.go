package zjmf

import (
	"context"
	"testing"
	"time"

	"ddyidc/internal/shared/testenv"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/cache"
)

func TestProductCacheSharedGeneration(t *testing.T) {
	client := testenv.Redis(t)
	previous := cache.RDB
	cache.RDB = client
	t.Cleanup(func() { cache.RDB = previous })
	a := New(contracts.Supplier{ID: "fixture", URL: "https://fixture.invalid", Username: "fixture", Token: "fixture"})
	b := New(a.Supplier)
	old, err := a.productCacheKey(1, "page")
	if err != nil {
		t.Fatal(err)
	}
	other, err := b.productCacheKey(1, "page")
	if err != nil || old != other {
		t.Fatal("nodes did not share generation", err)
	}
	if err := a.InvalidateProduct(1); err != nil {
		t.Fatal(err)
	}
	current, err := b.productCacheKey(1, "page")
	if err != nil || current == old {
		t.Fatal("remote node retained stale generation", err)
	}
	// A delayed response carries its original key and cannot populate current.
	b.cacheOrderPage(old, &contracts.OrderPage{})
	financeCache.Lock()
	_, reused := financeCache.pages[current]
	financeCache.Unlock()
	if reused {
		t.Fatal("late response populated current generation")
	}
	key := "upstream:product-generation:" + a.tokenKey() + ":1"
	if err := client.Del(context.Background(), key).Err(); err != nil {
		t.Fatal(err)
	}
	afterEviction, err := b.productCacheKey(1, "page")
	if err != nil || afterEviction == old || afterEviction == current {
		t.Fatal("eviction reused generation", err)
	}
	_ = client.Close()
	started := time.Now()
	if _, err := b.productCacheKey(1, "page"); err == nil {
		t.Fatal("Redis failure served stale cache")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("unbounded cache coordination")
	}
}

func TestSharedRequestSlot(t *testing.T) {
	client := testenv.Redis(t)
	previous := cache.RDB
	cache.RDB = client
	t.Cleanup(func() { cache.RDB = previous })
	ctx := context.Background()
	if err := waitForRequestSlotContext(ctx, "fixture.invalid", 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := waitForRequestSlotContext(ctx, "fixture.invalid", 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 60*time.Millisecond {
		t.Fatal("shared cooldown bypassed")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := waitForRequestSlotContext(cancelled, "fixture.invalid", time.Second); err == nil {
		t.Fatal("cancelled wait admitted")
	}
	_ = client.Close()
	if err := waitForRequestSlotContext(ctx, "fixture.invalid", time.Second); err == nil {
		t.Fatal("coordination failure admitted request")
	}
}
