// securityrecover is an explicit local-root escape hatch when all authenticators
// and recovery codes are lost. It never issues a login session or returns secrets.
package main

import (
	"context"
	"ddyidc/internal/security"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/db"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/zeromicro/go-zero/core/conf"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"os"
	"strings"
	"time"
)

func main() {
	config := flag.String("config", "", "existing runtime config")
	socket := flag.String("socket", "", "local root MySQL socket")
	reason := flag.String("reason", "", "required operator recovery reason")
	confirm := flag.Bool("disable-admin-mfa", false, "explicitly disable enforced administrator MFA")
	flag.Parse()
	if os.Geteuid() != 0 || *config == "" || *socket != "/var/run/mysqld/mysqld.sock" || !*confirm || len(strings.TrimSpace(*reason)) < 8 || len(*reason) > 200 {
		fail()
	}
	var c struct{ MySQL db.Config }
	if conf.Load(*config, &c) != nil || db.InitOfflineMigrationSocket(*socket, c.MySQL.Database, c.MySQL.Port) != nil {
		fail()
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if snowflake.Configure(ctx, db.DB) != nil {
		fail()
	}
	var restored security.Config
	e := db.G.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row struct{ Value string }
		if e := tx.Table("system.system_configs").Clauses(clause.Locking{Strength: "UPDATE"}).Select("value").Where("`key`=?", "security.admin_access").Take(&row).Error; e != nil {
			return e
		}
		if e := json.Unmarshal([]byte(row.Value), &restored); e != nil {
			return e
		}
		before := restored.MFAEnabled
		restored.MFAEnabled = false
		restored.Version++
		b, _ := json.Marshal(restored)
		if e := tx.Table("system.system_configs").Where("`key`=?", "security.admin_access").Update("value", string(b)).Error; e != nil {
			return e
		}
		detail, _ := json.Marshal(map[string]any{"reason": strings.TrimSpace(*reason), "beforeMfaEnabled": before, "afterMfaEnabled": false, "policyVersion": restored.Version})
		return tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": 3, "operator_id": 0, "action": "admin_mfa_emergency_disable", "target_type": "mfa", "detail": string(detail), "created_at": time.Now().UTC()}).Error
	})
	if e != nil {
		fail()
	}
	fmt.Println("Enforced administrator MFA disabled; existing sessions invalidated; factors retained. Rebind authenticators and re-enable MFA after sign-in.")
	fmt.Println("Administrator entry:", restored.AdminPath)
}
func fail() {
	fmt.Fprintln(os.Stderr, "Emergency recovery refused; requires local root, explicit socket, valid configuration and a recorded reason")
	os.Exit(1)
}
