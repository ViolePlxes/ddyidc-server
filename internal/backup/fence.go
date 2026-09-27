package backup

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"regexp"
	"strings"
	"time"
)

type RuntimeAccount struct {
	User          string `json:"user"`
	Host          string `json:"host"`
	AccountLocked string `json:"accountLocked"`
}

var accountHost = regexp.MustCompile(`^[A-Za-z0-9_.:%/-]+$`)

func accountSQL(a RuntimeAccount) string {
	return "'" + strings.ReplaceAll(a.User, "'", "''") + "'@'" + strings.ReplaceAll(a.Host, "'", "''") + "'"
}

// Only the dedicated application identity may be fenced. Maintenance uses a
// separate local root connection and business dumps never contain mysql.*.
func runtimeAccounts(ctx context.Context, database *gorm.DB, user string) ([]RuntimeAccount, error) {
	if user != "ddyidc_runtime" {
		return nil, errors.New("恢复要求使用独立 ddyidc_runtime 应用账户")
	}
	var accounts []RuntimeAccount
	if err := database.WithContext(ctx).Table("mysql.user").Select("User,Host,account_locked").Where("User=?", user).Find(&accounts).Error; err != nil {
		return nil, errors.New("无法核验应用账户隔离")
	}
	if len(accounts) == 0 {
		return nil, errors.New("应用账户不存在")
	}
	return accounts, nil
}

func fenceAccounts(ctx context.Context, database *gorm.DB, accounts []RuntimeAccount) error {
	if len(accounts) == 0 {
		return errors.New("缺少待隔离账户")
	}
	for _, a := range accounts {
		if a.User != "ddyidc_runtime" || !accountHost.MatchString(a.Host) {
			return errors.New("拒绝隔离非应用账户")
		}
		if err := database.WithContext(ctx).Exec("ALTER USER " + accountSQL(a) + " ACCOUNT LOCK").Error; err != nil {
			return errors.New("应用账户隔离失败，需检查维护记录")
		}
	}
	// ACCOUNT LOCK prevents fresh authentication, but doesn't close old sessions.
	// KILL existing application sessions before importing; root/backup sessions
	// are untouched. Unknown remote effects still require later reconciliation.
	var sessions []struct{ ID uint64 }
	if err := database.WithContext(ctx).Table("information_schema.PROCESSLIST").Select("ID").Where("USER='ddyidc_runtime'").Find(&sessions).Error; err != nil {
		return errors.New("读取应用连接失败")
	}
	for _, session := range sessions {
		if err := database.WithContext(ctx).Exec(fmt.Sprintf("KILL CONNECTION %d", session.ID)).Error; err != nil {
			// A concurrently closed session is harmless; confirm no application session
			// remains below rather than treating a gone ID as permission to proceed.
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	// MySQL KILL marks a thread for termination; its cleanup may outlive the
	// statement. Wait boundedly for actual disappearance, never infer isolation
	// from KILL success or unlock the account on timeout.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var remaining int64
		if err := database.WithContext(waitCtx).Table("information_schema.PROCESSLIST").Where("USER='ddyidc_runtime'").Count(&remaining).Error; err != nil {
			return errors.New("无法确认应用连接已退出，禁止导入")
		}
		if remaining == 0 {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return errors.New("仍有应用连接，禁止导入")
		case <-ticker.C:
		}
	}
}
