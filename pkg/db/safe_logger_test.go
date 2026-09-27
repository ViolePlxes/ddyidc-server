package db

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm/logger"
)

func TestSQLDiagnosticsDoNotLogValues(t *testing.T) {
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(previous)
	secret := "TEST_ONLY_SENSITIVE_VALUE"
	l := safeLogger{level: logger.Warn}
	l.Trace(context.Background(), time.Now(), func() (string, int64) { t.Fatal("SQL formatter must not be invoked"); return secret, 1 }, &mysqldriver.MySQLError{Number: 1062, Message: secret})
	if strings.Contains(out.String(), secret) || !strings.Contains(out.String(), "mysql_code=1062") || !strings.Contains(out.String(), "caller=") {
		t.Fatalf("unsafe or missing diagnostic: %q", out.String())
	}
}
