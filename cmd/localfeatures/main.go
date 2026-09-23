// localfeatures persists the user's all-open policy for the isolated development VM.
package main

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/db"
	"flag"
	"fmt"
	"github.com/zeromicro/go-zero/core/conf"
	"gorm.io/gorm"
	"os"
	"strings"
)

func main() {
	path := flag.String("config", "", "local VM configuration path")
	flag.Parse()
	if os.Getenv("DDYIDC_LOCAL_TEST_FEATURES") != "1" || *path == "" {
		fmt.Fprintln(os.Stderr, "local VM opt-in and configuration are required")
		os.Exit(1)
	}
	var c struct{ MySQL db.Config }
	conf.MustLoad(*path, &c)
	if c.MySQL.Host != "127.0.0.1" && c.MySQL.Host != "localhost" && c.MySQL.Host != "::1" {
		fmt.Fprintln(os.Stderr, "only a loopback test database is allowed")
		os.Exit(1)
	}
	if e := db.Init(c.MySQL); e != nil {
		fail()
	}
	defer db.Close()
	if e := snowflake.Configure(context.Background(), db.DB); e != nil {
		fail()
	}
	d := db.G
	var e error
	e = d.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
		for _, key := range []string{"groupbuy.enabled", "marketplace.enabled", "push_transfer.enabled", "payment.wechat.enabled", "payment.alipay.enabled", "marketing.invite.enabled", "marketing.invite.review_enabled", "marketing.reward.enabled", "marketing.reward.recharge_enabled", "marketing.promo.poster_enabled", "notify.expiry.enabled"} {
			if e := tx.Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,?,'true','本地测试环境功能全开') ON DUPLICATE KEY UPDATE value='true'", snowflake.GenID(), key).Error; e != nil {
				return e
			}
		}
		for _, q := range []string{
			"UPDATE system.points_settings SET enabled=1,show_mall=1,show_signin=1,show_tasks=1,streak_reward_enabled=1",
			"UPDATE system.marketplace_settings SET enabled=1,maintenance=0,refund_enabled=1,audit_enabled=1,withdrawal_audit_enabled=1",
			"UPDATE system.cdnsell_settings SET enabled=1,auto_suspend_enabled=1,auto_restore_enabled=1",
			"UPDATE system.cdnsell_p2_runtime SET cloud_writes_enabled=1,enabled_at=COALESCE(enabled_at,NOW()) WHERE id=1",
			"UPDATE system.video_platforms SET enabled=1",
			"UPDATE system.navigation_nodes SET enabled=1",
			"UPDATE system.navigation_scopes SET revision=revision+1",
			"UPDATE system.system_configs SET value=JSON_SET(value,'$.enabled',JSON_EXTRACT('true','$'),'$.companyEnabled',JSON_EXTRACT('true','$')) WHERE `key`='certification.settings'",
		} {
			if e := tx.Exec(q).Error; e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		fail()
	}
	fmt.Println(strings.Join([]string{"local VM feature policy applied", "groupbuy/marketplace/push/points/CDN/certification/video/payments/marketing/navigation enabled", "credentials, mock payments, authentication and individual business records unchanged"}, "; "))
}
func fail() {
	fmt.Fprintln(os.Stderr, "local feature policy failed; transaction rolled back (no credentials logged)")
	os.Exit(1)
}
