package metrics

import (
	"sync"
	"testing"
	"time"
)

func TestWindowsAndExpiry(t *testing.T) {
	c := New()
	now := time.Unix(1800, 0)
	c.now = func() time.Time { return now }
	c.Record("1")
	now = now.Add(2 * time.Second)
	c.Record("1")
	s := c.Snapshot()["1"]
	if s.OneMinute != 2 || *s.AverageInterval != 2 {
		t.Fatal(s)
	}
	now = time.Unix(1860, 0)
	s = c.Snapshot()["1"]
	if s.OneMinute != 0 || s.FiveMinutes != 2 {
		t.Fatal(s)
	}
	now = time.Unix(2100, 0)
	s = c.Snapshot()["1"]
	if s.FiveMinutes != 0 || s.FifteenMinutes != 2 {
		t.Fatal(s)
	}
	now = time.Unix(2700, 0)
	if len(c.Snapshot()) != 0 || len(c.windows) != 0 {
		t.Fatal("expired memory retained")
	}
}
func TestConcurrentSuppliers(t *testing.T) {
	c := New()
	c.now = func() time.Time { return time.Unix(1800, 0) }
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Record("a")
			}
		}()
	}
	wg.Wait()
	c.Record("b")
	s := c.Snapshot()
	if s["a"].OneMinute != 2000 || s["b"].OneMinute != 1 {
		t.Fatal(s)
	}
}
