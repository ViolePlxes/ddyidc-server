// bootstrapadmin is an explicit operator tool for a newly migrated empty system.
package main

import (
	"context"
	"ddyidc/internal/shared/migrations"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/db"
	"flag"
	"fmt"
	"github.com/zeromicro/go-zero/core/conf"
	"os"
	"strings"
	"time"
)

func main() {
	config := flag.String("config", "", "runtime database configuration")
	username := flag.String("username", "", "initial administrator username")
	passwordFile := flag.String("password-file", "", "operator supplied mode-0600 password file")
	flag.Parse()
	if *config == "" || *username == "" || *passwordFile == "" {
		fail()
	}
	info, err := os.Lstat(*passwordFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 74 {
		fail()
	}
	password, err := os.ReadFile(*passwordFile)
	if err != nil {
		fail()
	}
	password = []byte(strings.TrimRight(string(password), "\r\n"))
	var c struct{ MySQL db.Config }
	conf.MustLoad(*config, &c)
	c.MySQL.Migration = false
	if db.Init(c.MySQL) != nil {
		fail()
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	steps, err := migrations.Steps()
	if err != nil || migrations.Verify(ctx, db.DB, steps) != nil {
		fail()
	}
	if snowflake.Configure(ctx, db.DB) != nil {
		fail()
	}
	err = db.BootstrapAdmin(ctx, db.DB, *username, password)
	clear(password)
	if err != nil {
		fail()
	}
	fmt.Println("Initial administrator created; no default credentials were used")
}
func fail() {
	fmt.Fprintln(os.Stderr, "Administrator bootstrap refused; check migration, empty account table and protected input file (credentials suppressed)")
	os.Exit(1)
}
