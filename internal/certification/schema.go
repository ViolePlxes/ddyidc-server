package certification

import (
	"context"
	"ddyidc/internal/shared/snowflake"
	crypt "ddyidc/pkg/crypto"
	"errors"
	"fmt"
	"os"
	"strings"
)

func (s *Service) EnsureSchema(ctx context.Context) error {
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	var duplicates int64
	if err := s.db.WithContext(ctx).Raw("SELECT COUNT(*) FROM (SELECT customer_id FROM customer.customer_realname GROUP BY customer_id HAVING COUNT(*)>1) x").Scan(&duplicates).Error; err != nil {
		return err
	}
	if duplicates > 0 {
		return errors.New("实名记录存在重复客户；请先备份并人工核对，迁移不会删除记录")
	}
	columns := map[string]string{"phone": "varchar(32) NOT NULL DEFAULT ''", "document_type": "tinyint NOT NULL DEFAULT 1", "document_hash": "char(64) NOT NULL DEFAULT ''", "company_hash": "char(64) NOT NULL DEFAULT ''", "legal_type": "tinyint NOT NULL DEFAULT 1", "authorization": "varchar(255) NOT NULL DEFAULT ''"}
	for name, definition := range columns {
		var n int64
		if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema='customer' AND table_name='customer_realname' AND column_name=?", name).Scan(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			if err := s.db.Exec("ALTER TABLE customer.customer_realname ADD COLUMN " + name + " " + definition).Error; err != nil {
				return err
			}
		}
	}
	var length int
	if err := s.db.Raw("SELECT CHARACTER_MAXIMUM_LENGTH FROM information_schema.columns WHERE table_schema='customer' AND table_name='customer_realname' AND column_name='id_card_no'").Scan(&length).Error; err != nil {
		return err
	}
	if length < 512 {
		if err := s.db.Exec("ALTER TABLE customer.customer_realname MODIFY id_card_no varchar(512) DEFAULT NULL").Error; err != nil {
			return err
		}
	}
	var count int64
	if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema='customer' AND table_name='customer_realname' AND index_name='uk_realname_customer'").Scan(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		if err := s.db.Exec("ALTER TABLE customer.customer_realname ADD UNIQUE INDEX uk_realname_customer(customer_id)").Error; err != nil {
			return err
		}
	}
	// Never guess whether legacy document data is plaintext or encrypted.
	var legacy int64
	if err := s.db.Model(&Record{}).Where("COALESCE(id_card_no,'')<>'' AND document_hash='' OR COALESCE(company_license,'')<>'' AND company_hash=''").Count(&legacy).Error; err != nil {
		return err
	}
	if legacy > 0 {
		return fmt.Errorf("%d legacy certification records require explicit encrypted migration before enabling this module", legacy)
	}
	_, err := crypt.Encrypt("key-check", s.key)
	if err != nil {
		return err
	}
	if err := s.db.Exec("INSERT IGNORE INTO system.system_configs (id,`key`,value,description) VALUES (?, 'certification.settings', ?, '人工实名认证设置')", snowflake.GenID(), `{"enabled":false,"requirePurchase":false,"companyEnabled":false,"companyRequiresPerson":false}`).Error; err != nil {
		return err
	}
	// Idempotent navigation upgrade; existing navigation edits stay intact.
	for _, n := range []struct{ scope, href, name string }{{"console", "/console/certification", "实名认证"}, {"admin", "/admin/certifications", "实名审核"}} {
		var parent struct{ ID string }
		anchor := "/console/profile"
		if n.scope == "admin" {
			anchor = "/admin/customers"
		}
		if err := s.db.Table("system.navigation_nodes").Select("parent_id AS id").Where("scope=? AND href=?", n.scope, anchor).Take(&parent).Error; err != nil {
			if err := s.db.Table("system.navigation_nodes").Select("id").Where("scope=? AND parent_id=0 AND gate=''", n.scope).Order("sort ASC,id ASC").Take(&parent).Error; err != nil {
				return err
			}
		}
		var exists int64
		s.db.Table("system.navigation_nodes").Where("scope=? AND href=?", n.scope, n.href).Count(&exists)
		if exists > 0 {
			continue
		}
		id := fmt.Sprint(snowflake.GenID())
		err := s.db.Exec("INSERT INTO system.navigation_nodes (id,scope,parent_id,name,href,target,icon,gate,enabled,sort,created_at,updated_at) SELECT ?,?,?,?,?, 'self','UserIcon','',1,COALESCE(MAX(sort),0)+1,NOW(),NOW() FROM (SELECT sort FROM system.navigation_nodes WHERE scope=? AND parent_id=?) x", id, n.scope, parent.ID, n.name, n.href, n.scope, parent.ID).Error
		if err != nil && !strings.Contains(err.Error(), "Duplicate") {
			return err
		}
		if err == nil {
			s.db.Exec("UPDATE system.navigation_scopes SET revision=revision+1 WHERE scope=?", n.scope)
		}
	}
	return nil
}
