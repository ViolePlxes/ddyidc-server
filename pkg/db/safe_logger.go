package db

import (
	"context"
	"errors"
	"log"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SQL text and driver error messages can contain passwords, identity numbers,
// and duplicate key values. Only stable error codes and timings are logged.
type safeLogger struct{ level logger.LogLevel }

func (l safeLogger) LogMode(level logger.LogLevel) logger.Interface { l.level = level; return l }
func (safeLogger) Info(context.Context, string, ...interface{})     {}
func (safeLogger) Warn(context.Context, string, ...interface{})     {}
func (safeLogger) Error(context.Context, string, ...interface{})    {}
func (l safeLogger) Trace(_ context.Context, begin time.Time, _ func() (string, int64), err error) {
	if l.level == logger.Silent {
		return
	}
	elapsed := time.Since(begin)
	failed := err != nil && !errors.Is(err, gorm.ErrRecordNotFound)
	if !failed && elapsed < 200*time.Millisecond {
		return
	}
	var code uint16
	var mysqlError *mysqldriver.MySQLError
	if errors.As(err, &mysqlError) {
		code = mysqlError.Number
	}
	log.Printf("DB_QUERY failed=%t mysql_code=%d duration_ms=%d", failed, code, elapsed.Milliseconds())
}
