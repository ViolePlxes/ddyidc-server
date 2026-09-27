package main

import (
	"context"
	"ddyidc/internal/backup"
	"ddyidc/internal/shared/runtimepaths"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/db"
	"flag"
	"fmt"
	"github.com/zeromicro/go-zero/core/conf"
	"os"
	"time"
)

func main() {
	release := flag.String("release", "", "release a reconciled restore")
	verifyOSS := flag.String("verify-oss", "", "download and verify an OSS backup without restoring")
	confirm := flag.String("confirmation", "", "explicit external reconciliation confirmation")
	operator := flag.String("operator", "", "operations actor")
	reason := flag.String("reason", "", "release reason")
	flag.Parse()
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
	// The privileged worker must not create application-owned runtime paths.
	// Those are prepared by installation/the application identity.
	if e = paths.Within(paths.Root); e != nil {
		fmt.Fprintln(os.Stderr, "backup data directory unavailable")
		os.Exit(1)
	}
	if c.MySQL.Host != "127.0.0.1" && c.MySQL.Host != "localhost" && c.MySQL.Host != "::1" {
		fmt.Fprintln(os.Stderr, "backup worker requires the configured local database")
		os.Exit(1)
	}
	pool, database, e := db.OpenMaintenanceSocket("/var/run/mysqld/mysqld.sock")
	if e != nil {
		fmt.Fprintln(os.Stderr, "backup maintenance database unavailable")
		os.Exit(1)
	}
	defer pool.Close()
	var port int
	if e = pool.QueryRow("SELECT @@port").Scan(&port); e != nil || port != c.MySQL.Port {
		fmt.Fprintln(os.Stderr, "backup socket does not match configured local database")
		os.Exit(1)
	}
	if e = snowflake.Configure(context.Background(), pool); e != nil {
		fmt.Fprintln(os.Stderr, "backup ID allocator unavailable")
		os.Exit(1)
	}
	cache.MustInit(c.Redis)
	defer cache.RDB.Close()
	service := backup.New(database, []byte(c.Crypto.AESKey), paths.Root)
	service.SetRuntimeAccount(c.MySQL.User)
	if e = service.ConfigureMaintenance(); e != nil {
		fmt.Fprintln(os.Stderr, "root-only maintenance directory or independent signing key unavailable")
		os.Exit(1)
	}
	if e = service.PrepareWorkerEnvironment(); e != nil {
		fmt.Fprintln(os.Stderr, "backup private environment unavailable")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	if *verifyOSS != "" {
		if *release != "" {
			fmt.Fprintln(os.Stderr, "verification and release are mutually exclusive")
			os.Exit(1)
		}
		if e = service.VerifyOSS(ctx, *verifyOSS); e != nil {
			fmt.Fprintln(os.Stderr, "OSS archive verification failed:", e)
			os.Exit(1)
		}
		fmt.Println("OSS download, archive hashes, independent signature, AES fingerprint and application/schema compatibility verified; no restore performed")
		return
	}
	if *release != "" {
		if e = service.ReleaseRestore(ctx, *release, *confirm, *operator, *reason); e != nil {
			fmt.Fprintln(os.Stderr, "restore release rejected; check reconciliation and maintenance journal")
			os.Exit(1)
		}
		fmt.Println("restore release authorized; start application services after checking the maintenance journal")
		return
	}
	if e = service.Worker(ctx); e != nil {
		fmt.Fprintln(os.Stderr, "backup worker failed; see sanitized admin history")
		os.Exit(1)
	}
	fmt.Println("backup worker completed")
}
