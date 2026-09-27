// Package filebarrier serializes attachment removal against backup copying.
// Published attachment names are immutable; new uploads may continue while a
// snapshot runs, but collecting old files must wait until copying finishes.
package filebarrier

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"gorm.io/gorm"
	"time"
)

var ErrBusy = errors.New("附件备份正在进行，稍后回收")

const name = "ddyidc.storage.snapshot"

func With(ctx context.Context, database *gorm.DB, fn func(context.Context) error) error {
	pool, err := database.DB()
	if err != nil {
		return err
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?,0)", name).Scan(&acquired); err != nil {
		return err
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return ErrBusy
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := conn.ExecContext(release, "DO RELEASE_LOCK(?)", name); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	return fn(ctx)
}
