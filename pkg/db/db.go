package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// Config 数据库配置
type Config struct {
	Host            string
	Port            int
	User            string
	Password        string
	Database        string
	Migration       bool `json:",optional"`
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime int
}

var DB *sql.DB

// G is the single GORM connection pool. Repositories must select their schema
// explicitly with Table("schema.table") instead of relying on a default schema.
var G *gorm.DB

func dsn(c Config, database string) string {
	cfg := mysqldriver.NewConfig()
	cfg.User, cfg.Passwd, cfg.Net = c.User, c.Password, "tcp"
	cfg.Addr, cfg.DBName = net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), database
	cfg.ParseTime, cfg.Loc, cfg.MultiStatements = true, time.Local, c.Migration
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
	cfg.Params = map[string]string{"charset": "utf8mb4"}
	return cfg.FormatDSN()
}

// Init connects without DDL. Only an explicit offline migration may create schemas. It
// exposes both database/sql (legacy code) and GORM (new repositories).
func Init(c Config) error {
	if strings.TrimSpace(c.Database) == "" {
		return errors.New("mysql database must not be empty")
	}
	if c.Migration {
		bootstrap, err := sql.Open("mysql", dsn(c, ""))
		if err != nil {
			return fmt.Errorf("open mysql bootstrap connection: %w", err)
		}
		defer bootstrap.Close()
		if err := bootstrap.Ping(); err != nil {
			return fmt.Errorf("ping mysql %s:%d: %w", c.Host, c.Port, err)
		}
		var existing string
		lookupErr := bootstrap.QueryRow("SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", c.Database).Scan(&existing)
		if errors.Is(lookupErr, sql.ErrNoRows) {
			if _, err := bootstrap.Exec("CREATE DATABASE `" + strings.ReplaceAll(c.Database, "`", "``") + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
				return fmt.Errorf("primary schema %q is missing and could not be created: %w", c.Database, err)
			}
		} else if lookupErr != nil {
			return fmt.Errorf("check primary schema %q: %w", c.Database, lookupErr)
		}

	}
	g, err := gorm.Open(mysql.Open(dsn(c, c.Database)), &gorm.Config{
		Logger:         safeLogger{level: logger.Warn},
		NamingStrategy: schema.NamingStrategy{SingularTable: false},
	})
	if err != nil {
		return fmt.Errorf("open gorm mysql connection: %w", err)
	}
	sqlDB, err := g.DB()
	if err != nil {
		return fmt.Errorf("get gorm sql database: %w", err)
	}
	sqlDB.SetMaxOpenConns(c.MaxOpenConns)
	sqlDB.SetMaxIdleConns(c.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(c.ConnMaxLifetime) * time.Second)
	if err := sqlDB.Ping(); err != nil {
		return fmt.Errorf("ping gorm mysql connection: %w", err)
	}
	DB, G = sqlDB, g
	return nil
}

// MustInit is kept for existing callers. It fails with an actionable message.
func MustInit(c Config) {
	if err := Init(c); err != nil {
		panic("database initialization failed: " + err.Error())
	}
}

func Close() error {
	if DB == nil {
		return nil
	}
	return DB.Close()
}
