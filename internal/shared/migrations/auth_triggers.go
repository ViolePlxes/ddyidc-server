package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

var authTrigger = regexp.MustCompile("(?i)^CREATE TRIGGER IF NOT EXISTS `([^`]+)`\\.`([^`]+)` BEFORE UPDATE ON `([^`]+)`\\.`([^`]+)` FOR EACH ROW (.+);$")

func normalizedSQL(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '`' {
			return -1
		}
		return unicode.ToLower(r)
	}, strings.TrimSuffix(strings.TrimSpace(value), ";"))
}

// MySQL checks binary-log SUPER requirements even for CREATE TRIGGER IF NOT
// EXISTS. An already installed, exactly matching trigger is adopted read-only;
// do not request broader privileges or change the global binary-log policy.
// This special handling is limited to the three one-statement auth triggers.
func authSQL(ctx context.Context, conn *sql.Conn, script string) (string, error) {
	lines := strings.Split(script, "\n")
	for i, line := range lines {
		match := authTrigger.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		if match[1] != match[3] {
			return "", fmt.Errorf("invalid auth trigger schema")
		}
		var table, event, timing, body string
		err := conn.QueryRowContext(ctx, "SELECT EVENT_OBJECT_TABLE,EVENT_MANIPULATION,ACTION_TIMING,ACTION_STATEMENT FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=? AND TRIGGER_NAME=?", match[1], match[2]).Scan(&table, &event, &timing, &body)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return "", err
		}
		if table != match[4] || event != "UPDATE" || timing != "BEFORE" || normalizedSQL(body) != normalizedSQL(match[5]) {
			return "", fmt.Errorf("auth trigger %s differs; DBA review required", match[2])
		}
		lines[i] = "-- Existing auth trigger verified; no DDL executed."
	}
	return strings.Join(lines, "\n"), nil
}
