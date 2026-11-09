package visitanalytics

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Partition maintenance is offline-only. MySQL DDL requires the migration
// identity, so the monthly partitions are extended and expired ones are dropped
// during the reviewed migration run; the application never issues DDL.
const (
	partitionPrefix = "p"
	partitionMax    = "pmax"
	// partitionHorizonMonths keeps the writer well ahead of the calendar.
	partitionHorizonMonths = 6
)

// PartitionReport describes what the offline run changed.
type PartitionReport struct {
	Added   []string
	Dropped []string
	Highest string
}

type partition struct {
	Name  string
	Upper int64 // TO_DAYS bound; zero for the MAXVALUE catch-all
	Max   bool
}

// EnsurePartitions extends the monthly partitions and drops expired ones.
func EnsurePartitions(ctx context.Context, database *sql.DB, now time.Time, retentionDays int) (PartitionReport, error) {
	var report PartitionReport
	if retentionDays <= 0 || retentionDays > maxRetentionDays {
		retentionDays = defaultRetentionDays
	}
	partitions, err := readPartitions(ctx, database)
	if err != nil {
		return report, err
	}
	if len(partitions) == 0 {
		return report, fmt.Errorf("system.visit_events is not partitioned")
	}
	hasCatchAll := false
	monthly := make(map[int64]string, len(partitions))
	for _, item := range partitions {
		if item.Max {
			hasCatchAll = true
			continue
		}
		monthly[item.Upper] = item.Name
	}
	if !hasCatchAll {
		return report, fmt.Errorf("system.visit_events is missing its MAXVALUE partition")
	}
	// Extend the horizon first so new rows never land in the catch-all.
	start := monthStart(now).AddDate(0, -1, 0)
	for index := 0; index <= partitionHorizonMonths; index++ {
		month := start.AddDate(0, index, 0)
		upper, err := toDays(ctx, database, month.AddDate(0, 1, 0))
		if err != nil {
			return report, err
		}
		if _, exists := monthly[upper]; exists {
			continue
		}
		name := partitionPrefix + month.Format("200601")
		statement := fmt.Sprintf("ALTER TABLE system.visit_events REORGANIZE PARTITION %s INTO (PARTITION %s VALUES LESS THAN (%d), PARTITION %s VALUES LESS THAN MAXVALUE)", partitionMax, name, upper, partitionMax)
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return report, fmt.Errorf("extend visit event partitions: %w", err)
		}
		monthly[upper] = name
		report.Added = append(report.Added, name)
	}
	// Drop whole partitions that fall entirely outside the retention window.
	cutoff, err := toDays(ctx, database, now.AddDate(0, 0, -retentionDays))
	if err != nil {
		return report, err
	}
	for _, item := range expiredPartitions(monthly, cutoff) {
		if _, err := database.ExecContext(ctx, "ALTER TABLE system.visit_events DROP PARTITION "+item.Name); err != nil {
			return report, fmt.Errorf("drop expired visit event partition: %w", err)
		}
		delete(monthly, item.Upper)
		report.Dropped = append(report.Dropped, item.Name)
	}
	bounds := make([]int64, 0, len(monthly))
	for upper := range monthly {
		bounds = append(bounds, upper)
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
	if len(bounds) > 0 {
		highest, err := fromToDays(ctx, database, bounds[len(bounds)-1])
		if err != nil {
			return report, err
		}
		report.Highest = highest
	}
	return report, nil
}

// PartitionHorizon reports the latest covered day, used for the startup warning.
func PartitionHorizon(ctx context.Context, database *sql.DB) (time.Time, error) {
	partitions, err := readPartitions(ctx, database)
	if err != nil {
		return time.Time{}, err
	}
	highest := int64(0)
	for _, item := range partitions {
		if item.Max {
			continue
		}
		if item.Upper > highest {
			highest = item.Upper
		}
	}
	if highest == 0 {
		return time.Time{}, nil
	}
	raw, err := fromToDays(ctx, database, highest)
	if err != nil {
		return time.Time{}, err
	}
	return time.ParseInLocation("2006-01-02", raw, time.Local)
}

// expiredPartitions selects partitions whose whole range is older than the
// cutoff, ordered oldest first so a failed drop leaves a consistent tail.
func expiredPartitions(monthly map[int64]string, cutoff int64) []partition {
	result := make([]partition, 0, len(monthly))
	for upper, name := range monthly {
		if upper <= cutoff {
			result = append(result, partition{Name: name, Upper: upper})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Upper < result[j].Upper })
	return result
}

func readPartitions(ctx context.Context, database *sql.DB) ([]partition, error) {
	rows, err := database.QueryContext(ctx, "SELECT PARTITION_NAME, PARTITION_DESCRIPTION FROM information_schema.PARTITIONS WHERE TABLE_SCHEMA='system' AND TABLE_NAME='visit_events' AND PARTITION_NAME IS NOT NULL ORDER BY PARTITION_ORDINAL_POSITION")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]partition, 0, 8)
	for rows.Next() {
		var name, description sql.NullString
		if err = rows.Scan(&name, &description); err != nil {
			return nil, err
		}
		item := partition{Name: name.String}
		if strings.EqualFold(strings.TrimSpace(description.String), "MAXVALUE") {
			item.Max = true
		} else if upper, err := strconv.ParseInt(strings.TrimSpace(description.String), 10, 64); err == nil {
			item.Upper = upper
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func toDays(ctx context.Context, database *sql.DB, day time.Time) (int64, error) {
	var value int64
	if err := database.QueryRowContext(ctx, "SELECT TO_DAYS(?)", day.Format("2006-01-02")).Scan(&value); err != nil {
		return 0, err
	}
	return value, nil
}

func fromToDays(ctx context.Context, database *sql.DB, days int64) (string, error) {
	var value sql.NullString
	if err := database.QueryRowContext(ctx, "SELECT DATE_FORMAT(FROM_DAYS(?), '%Y-%m-%d')", days).Scan(&value); err != nil {
		return "", err
	}
	return value.String, nil
}

func monthStart(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, value.Location())
}

// StartMaintenance keeps the pre-aggregates pruned and the geo database fresh.
// It is registered as a cron entry point.
func (s *Service) StartMaintenance(ctx context.Context) {
	if s == nil || s.database == nil {
		return
	}
	if s.Enabled() {
		s.StartWriter(ctx)
		s.StartNginxLog(ctx)
	}
	s.reloadGeo()
	if highest, err := PartitionHorizon(ctx, s.database); err == nil && !highest.IsZero() {
		if days := int(highest.Sub(time.Now()).Hours() / 24); days < 30 {
			log.Printf("visit_analytics_partition_horizon_short remaining_days=%d; run the offline migration before the table overflows", days)
		}
	}
	geoInterval := time.Duration(s.cfg.GeoIPReloadMinutes) * time.Minute
	nextGeo := time.Now().Add(geoInterval)
	lastPrune := ""
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if now.After(nextGeo) {
				s.reloadGeo()
				nextGeo = now.Add(geoInterval)
			}
			today := now.Format("2006-01-02")
			if lastPrune == today || now.Hour() < 4 {
				continue
			}
			if _, err := pruneDaily(ctx, s.database, s.cfg.RetentionDays, now); err != nil {
				log.Printf("visit_analytics_prune_failed error=%v", err)
				continue
			}
			lastPrune = today
		}
	}
}

func (s *Service) reloadGeo() {
	if s == nil || s.geo == nil {
		return
	}
	changed, err := s.geo.reload()
	if err != nil {
		log.Printf("visit_analytics_geo_reload_failed error=%v", err)
		return
	}
	if changed {
		log.Printf("visit_analytics_geo_reloaded path=%s", s.cfg.GeoIPFile)
	}
}
