package db

import (
	"context"
	"errors"
	"log"
	"runtime"
	"strings"
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
	caller, line := queryCaller()
	log.Printf("DB_QUERY failed=%t mysql_code=%d duration_ms=%d caller=%s line=%d", failed, code, elapsed.Milliseconds(), caller, line)
}

// Identify the application callsite without formatting SQL or driver messages.
// Runtime symbols contain compiled identifiers, never query values or request data.
func queryCaller() (string, int) {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if strings.HasPrefix(frame.Function, "ddyidc/") && !strings.HasPrefix(frame.Function, "ddyidc/pkg/db.") {
			return frame.Function, frame.Line
		}
		if !more {
			break
		}
	}
	return "unknown", 0
}
