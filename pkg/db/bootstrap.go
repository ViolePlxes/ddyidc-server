package db

import (
	"context"
	"database/sql"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"regexp"
	"time"
)

// BootstrapAdmin is an explicit offline installation action. It cannot reset or
// replace an existing administrator and has no built-in username or password.
func BootstrapAdmin(ctx context.Context, pool *sql.DB, username string, password []byte) error {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`).MatchString(username) || len(password) < 12 || len(password) > 72 {
		return errors.New("invalid bootstrap username or password length")
	}
	hash, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if err != nil {
		return errors.New("password hashing failed")
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('ddyidc.admin.bootstrap',0)").Scan(&locked); err != nil || locked != 1 {
		return errors.New("bootstrap already running")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, "SELECT RELEASE_LOCK('ddyidc.admin.bootstrap')")
	}()
	var count int
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM system.admins").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("administrator already exists; bootstrap refused")
	}
	_, err = conn.ExecContext(ctx, "INSERT INTO system.admins(id,username,password_hash,real_name,status,is_super,auth_version) VALUES(?,?,?,'系统管理员',1,1,1)", snowflake.GenID(), username, string(hash))
	return err
}
