// Package visitanalytics collects privacy-bounded visit events and serves the
// administrator aggregates behind the admin home visit panel.
package visitanalytics

import (
	"errors"
	"strings"
	"time"
)

// Config controls collection, retention and the optional offline geo database.
// Zero values keep the documented defaults; Disabled is the only kill switch.
type Config struct {
	Disabled           bool    `json:",optional"`
	PageSamplePercent  float64 `json:",optional"`
	APISamplePercent   float64 `json:",optional"`
	MaxEventsPerSecond int     `json:",optional"`
	BatchSize          int     `json:",optional"`
	FlushMillis        int     `json:",optional"`
	QueueSize          int     `json:",optional"`
	RetentionDays      int     `json:",optional"`
	GeoIPFile          string  `json:",optional"`
	GeoIPReloadMinutes int     `json:",optional"`
}

const (
	defaultPageSample    = 100.0
	defaultAPISample     = 10.0
	defaultMaxPerSecond  = 5000
	defaultBatchSize     = 200
	defaultFlushMillis   = 2000
	defaultQueueSize     = 8192
	defaultRetentionDays = 90
	defaultReloadMinutes = 60
	maxBatchSize         = 1000
	maxQueueSize         = 65536
	maxRetentionDays     = 90
)

// normalized clamps operator input into the reviewed envelope. A zero sample
// rate means "unset": use Disabled to stop collection entirely.
func (c Config) normalized() Config {
	if c.PageSamplePercent <= 0 || c.PageSamplePercent > 100 {
		c.PageSamplePercent = defaultPageSample
	}
	if c.APISamplePercent <= 0 || c.APISamplePercent > 100 {
		c.APISamplePercent = defaultAPISample
	}
	if c.MaxEventsPerSecond <= 0 {
		c.MaxEventsPerSecond = defaultMaxPerSecond
	}
	if c.BatchSize <= 0 || c.BatchSize > maxBatchSize {
		c.BatchSize = defaultBatchSize
	}
	if c.FlushMillis <= 0 {
		c.FlushMillis = defaultFlushMillis
	}
	if c.QueueSize <= 0 || c.QueueSize > maxQueueSize {
		c.QueueSize = defaultQueueSize
	}
	// Retention beyond 90 days is never allowed: the raw table is partitioned
	// monthly and expired partitions are dropped by the offline migration run.
	if c.RetentionDays <= 0 || c.RetentionDays > maxRetentionDays {
		c.RetentionDays = defaultRetentionDays
	}
	if c.GeoIPReloadMinutes <= 0 {
		c.GeoIPReloadMinutes = defaultReloadMinutes
	}
	c.GeoIPFile = strings.TrimSpace(c.GeoIPFile)
	return c
}

func (c Config) flushInterval() time.Duration { return time.Duration(c.FlushMillis) * time.Millisecond }

// Validate rejects configurations that cannot be honoured instead of silently
// degrading the privacy or retention guarantees.
func (c Config) Validate() error {
	if c.GeoIPFile != "" && !strings.HasPrefix(c.GeoIPFile, "/") {
		return errors.New("visit analytics geo database must be an absolute path")
	}
	return nil
}
