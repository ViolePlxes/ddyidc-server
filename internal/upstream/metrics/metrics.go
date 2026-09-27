// Package metrics observes provider HTTP attempts in process memory only.
package metrics

import (
	"net/http"
	"sync"
	"time"
)

type bucket struct {
	Minute      int64
	Count       int64
	Failures    int64
	First, Last time.Time
}
type Counter struct {
	mu      sync.Mutex
	windows map[string]*[15]bucket
	now     func() time.Time
}
type Summary struct {
	OneMinute            int64    `json:"oneMinute"`
	FiveMinutes          int64    `json:"fiveMinutes"`
	FifteenMinutes       int64    `json:"fifteenMinutes"`
	FailedFifteenMinutes int64    `json:"failedFifteenMinutes"`
	AverageInterval      *float64 `json:"averageInterval"`
}

func New() *Counter { return &Counter{windows: make(map[string]*[15]bucket), now: time.Now} }

var Default = New()

func (c *Counter) prune(minute int64) {
	for id, window := range c.windows {
		active := false
		for i := range window {
			if window[i].Count > 0 && window[i].Minute > minute-15 {
				active = true
			} else {
				window[i] = bucket{}
			}
		}
		if !active {
			delete(c.windows, id)
		}
	}
}
func (c *Counter) Record(id string) time.Time {
	if id == "" {
		return time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	minute := now.Unix() / 60
	c.prune(minute)
	window := c.windows[id]
	if window == nil {
		window = &[15]bucket{}
		c.windows[id] = window
	}
	b := &window[minute%15]
	if b.Minute != minute || b.Count == 0 {
		*b = bucket{Minute: minute, First: now}
	}
	b.Count++
	b.Last = now
	return now
}

func (c *Counter) failure(id string, started time.Time) {
	if id == "" || started.IsZero() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	minute := started.Unix() / 60
	if window := c.windows[id]; window != nil {
		b := &window[minute%15]
		if b.Minute == minute && b.Count > 0 {
			b.Failures++
		}
	}
}
func (c *Counter) Snapshot() map[string]Summary {
	c.mu.Lock()
	defer c.mu.Unlock()
	minute := c.now().Unix() / 60
	c.prune(minute)
	out := make(map[string]Summary, len(c.windows))
	for id, w := range c.windows {
		var s Summary
		var first, last time.Time
		for _, b := range w {
			if b.Count == 0 {
				continue
			}
			age := minute - b.Minute
			if age < 0 || age >= 15 {
				continue
			}
			s.FifteenMinutes += b.Count
			s.FailedFifteenMinutes += b.Failures
			if age < 5 {
				s.FiveMinutes += b.Count
			}
			if age < 1 {
				s.OneMinute += b.Count
			}
			if first.IsZero() || b.First.Before(first) {
				first = b.First
			}
			if b.Last.After(last) {
				last = b.Last
			}
		}
		if s.FifteenMinutes > 1 {
			v := last.Sub(first).Seconds() / float64(s.FifteenMinutes-1)
			s.AverageInterval = &v
		}
		out[id] = s
	}
	return out
}

// Count at RoundTrip, including retries, redirects and failed HTTP attempts.
// Cache hits never reach this transport. No URLs, payloads or credentials kept.
type Transport struct {
	Base       http.RoundTripper
	SupplierID string
}

func (t Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	started := Default.Record(t.SupplierID)
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(r)
	if err != nil || response == nil || response.StatusCode >= 400 {
		Default.failure(t.SupplierID, started)
	}
	return response, err
}
