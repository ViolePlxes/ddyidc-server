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

// OpenControl uses an independent pool: long-lived maintenance read locks must
// never consume the business connections needed to finish their own requests.
func OpenControl(c Config) (*sql.DB, error) {
	c.Migration = false
	pool, err := sql.Open("mysql", dsn(c, c.Database))
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(32)
	pool.SetMaxIdleConns(4)
	pool.SetConnMaxLifetime(5 * time.Minute)
	if err := pool.Ping(); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// OpenMaintenanceSocket is only for the fixed privileged local backup worker.
// It must not share the application account that is fenced during restore.
func OpenMaintenanceSocket(socket string) (*sql.DB, *gorm.DB, error) {
	return openLocalSocket(socket, false)
}

// InitOfflineMigrationSocket is used only by the explicit root-only migration
// CLI. It never grants SUPER to the persistent migration/runtime identities.
func InitOfflineMigrationSocket(socket, primary string, expectedPort int) error {
	if strings.TrimSpace(primary) == "" {
		return errors.New("migration primary schema is empty")
	}
	pool, database, err := openLocalSocket(socket, true)
	if err != nil {
		return err
	}
	var port int
	if err := pool.QueryRow("SELECT @@port").Scan(&port); err != nil || port != expectedPort {
		pool.Close()
		return errors.New("migration socket does not match configured database")
	}
	if _, err := pool.Exec("CREATE DATABASE IF NOT EXISTS `" + strings.ReplaceAll(primary, "`", "``") + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		pool.Close()
		return err
	}
	DB, G = pool, database
	return nil
}

func openLocalSocket(socket string, multiStatements bool) (*sql.DB, *gorm.DB, error) {
	cfg := mysqldriver.NewConfig()
	cfg.User, cfg.Net, cfg.Addr = "root", "unix", socket
	cfg.MultiStatements = multiStatements
	cfg.ParseTime, cfg.Loc = true, time.Local
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 30*time.Second, 30*time.Second
	pool, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, nil, err
	}
	pool.SetMaxOpenConns(8)
	pool.SetMaxIdleConns(2)
	if err := pool.Ping(); err != nil {
		pool.Close()
		return nil, nil, err
	}
	database, err := gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	return pool, database, nil
}

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
