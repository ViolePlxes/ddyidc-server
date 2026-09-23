package main

import (
	"context"
	"ddyidc/internal/backup"
	"ddyidc/internal/shared/runtimepaths"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/db"
	"fmt"
	"github.com/zeromicro/go-zero/core/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"time"
)

func main() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "backup worker requires the dedicated root systemd unit")
		os.Exit(1)
	}
	var c struct {
		MySQL   db.Config
		Redis   cache.Config
		Crypto  struct{ AESKey string }
		Runtime runtimepaths.Config `json:",optional"`
	}
	conf.MustLoad("/etc/ddyidc/config.yaml", &c)
	paths, e := runtimepaths.Resolve(c.Runtime)
	if e != nil {
		os.Exit(1)
	}
	if e = paths.Prepare(); e != nil {
		fmt.Fprintln(os.Stderr, "backup data directory unavailable")
		os.Exit(1)
	}
	if e = db.Init(c.MySQL); e != nil {
		fmt.Fprintln(os.Stderr, "backup database unavailable")
		os.Exit(1)
	}
	defer db.Close()
	if e = snowflake.Configure(context.Background(), db.DB); e != nil {
		fmt.Fprintln(os.Stderr, "backup ID allocator unavailable")
		os.Exit(1)
	}
	cache.MustInit(c.Redis)
	defer cache.RDB.Close()
	service := backup.New(db.G.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}), []byte(c.Crypto.AESKey), paths.Root)
	if e = service.PreparePaths(); e != nil {
		fmt.Fprintln(os.Stderr, "backup schema unavailable")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	if e = service.Worker(ctx); e != nil {
		fmt.Fprintln(os.Stderr, "backup worker failed; see sanitized admin history")
		os.Exit(1)
	}
	fmt.Println("backup worker completed")
}
