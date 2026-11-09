package visitanalytics

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// eventColumns lists the persisted event columns in insertion order. The
// identifier and created_at are assigned by the database.
var eventColumns = []string{
	"occurred_at", "request_id", "host", "method", "path", "route",
	"referer_host", "referer_path", "page_title", "identity_type", "identity_id",
	"status_code", "duration_ms", "client_type", "browser", "os", "device",
	"is_bot", "ip_hash", "country", "region", "city",
}

// dailyAggregate is one accumulated pre-aggregate slice inside a batch.
type dailyAggregate struct {
	requests int64
	errors   int64
	visitors map[string]struct{}
}

type dailyKey struct {
	day       string
	dimension string
	value     string
}

// insertEvents writes the raw batch. Failure keeps the whole batch out of both
// tables because the caller wraps the calls in one transaction.
func insertEvents(ctx context.Context, transaction *sql.Tx, batch []Record) error {
	if len(batch) == 0 {
		return nil
	}
	placeholder := "(" + strings.TrimSuffix(strings.Repeat("?,", len(eventColumns)), ",") + ")"
	statement := "INSERT INTO system.visit_events (" + strings.Join(eventColumns, ",") + ") VALUES " +
		strings.TrimSuffix(strings.Repeat(placeholder+",", len(batch)), ",")
	arguments := make([]any, 0, len(batch)*len(eventColumns))
	for _, record := range batch {
		var identity any
		if record.IdentityID > 0 {
			identity = record.IdentityID
		}
		var isBot int
		if record.IsBot {
			isBot = 1
		}
		arguments = append(arguments,
			record.OccurredAt, record.RequestID, record.Host, record.Method, record.Path, record.Route,
			record.RefererHost, record.RefererPath, record.PageTitle, record.IdentityType, identity,
			record.StatusCode, record.DurationMS, record.ClientType, record.Browser, record.OS,
			record.Device, isBot, record.IPHash, record.Country, record.Region, record.City,
		)
	}
	if _, err := transaction.ExecContext(ctx, statement, arguments...); err != nil {
		return fmt.Errorf("insert visit events: %w", err)
	}
	return nil
}

// aggregateBatch folds a batch into per-day, per-dimension counters.
func aggregateBatch(batch []Record) map[dailyKey]*dailyAggregate {
	aggregates := make(map[dailyKey]*dailyAggregate, len(batch)*4)
	for _, record := range batch {
		day := record.OccurredAt.Format("2006-01-02")
		failed := record.StatusCode >= 500
		for _, row := range record.aggregateRows() {
			key := dailyKey{day: day, dimension: row.Dimension, value: row.Value}
			aggregate := aggregates[key]
			if aggregate == nil {
				aggregate = &dailyAggregate{visitors: make(map[string]struct{}, 1)}
				aggregates[key] = aggregate
			}
			aggregate.requests++
			if failed {
				aggregate.errors++
			}
			if record.IPHash != "" {
				aggregate.visitors[record.IPHash] = struct{}{}
			}
		}
	}
	return aggregates
}

// upsertDaily folds the batch into the pre-aggregate table. Visitors are
// de-duplicated inside a batch only, which is documented in the API notes.
func upsertDaily(ctx context.Context, transaction *sql.Tx, batch []Record) error {
	if len(batch) == 0 {
		return nil
	}
	aggregates := aggregateBatch(batch)
	const chunk = 300
	keys := make([]dailyKey, 0, len(aggregates))
	for key := range aggregates {
		keys = append(keys, key)
	}
	for start := 0; start < len(keys); start += chunk {
		end := start + chunk
		if end > len(keys) {
			end = len(keys)
		}
		placeholder := "(?,?,?,?,?,?)"
		statement := "INSERT INTO system.visit_daily (day,dimension,dimension_value,requests,visitors,errors) VALUES " +
			strings.TrimSuffix(strings.Repeat(placeholder+",", end-start), ",") +
			" ON DUPLICATE KEY UPDATE requests=requests+VALUES(requests),visitors=visitors+VALUES(visitors),errors=errors+VALUES(errors)"
		arguments := make([]any, 0, (end-start)*6)
		for _, key := range keys[start:end] {
			aggregate := aggregates[key]
			arguments = append(arguments, key.day, key.dimension, key.value, aggregate.requests, int64(len(aggregate.visitors)), aggregate.errors)
		}
		if _, err := transaction.ExecContext(ctx, statement, arguments...); err != nil {
			return fmt.Errorf("upsert visit daily: %w", err)
		}
	}
	return nil
}

// pruneDaily removes pre-aggregates outside the retention window. Raw events are
// removed by dropping whole month partitions during the offline migration run.
func pruneDaily(ctx context.Context, database *sql.DB, retentionDays int, now time.Time) (int64, error) {
	cutoff := now.AddDate(0, 0, -retentionDays).Format("2006-01-02")
	result, err := database.ExecContext(ctx, "DELETE FROM system.visit_daily WHERE day < ?", cutoff)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return affected, nil
}
