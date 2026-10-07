package points

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"regexp"
	"sort"
	"strings"
)

var adminEventPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

func (s *Service) AdminTriggerRequest(ctx context.Context, actor int64, ip, request string, users []int64, event string, cents int64, notes string) (int, error) {
	if actor <= 0 || ip == "" || len(ip) > 64 || !exchangeRequestPattern.MatchString(request) || len(users) == 0 || len(users) > 100 || !adminEventPattern.MatchString(event) || cents < 0 || cents > 100000000 || strings.TrimSpace(notes) == "" || len(notes) > 500 {
		return 0, errors.New("人工触发需明确用户、事件、金额、操作原因与请求标识，最多100个用户")
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	for i, user := range users {
		if user <= 0 || (i > 0 && user == users[i-1]) {
			return 0, errors.New("用户编号无效或重复")
		}
	}
	body, _ := json.Marshal(struct {
		Users []int64
		Event string
		Cents int64
		Notes string
	}{users, event, cents, notes})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(body))
	affected := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var admin struct{ ID int64 }
		if err := tx.Table("system.admins").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=1", actor).Take(&admin).Error; err != nil {
			return errors.New("操作管理员无效")
		}
		var old struct {
			Fingerprint string
			Affected    int
		}
		err := tx.Table("system.points_admin_actions").Where("admin_id=? AND request_id=?", actor, request).Take(&old).Error
		if err == nil {
			if old.Fingerprint != fingerprint {
				return errors.New("操作请求标识冲突")
			}
			affected = old.Affected
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var configured int64
		var enabled bool
		if err = tx.Table(settingsTable).Select("enabled").Where("id=1").Scan(&enabled).Error; err != nil {
			return err
		}
		if !enabled {
			return errors.New("积分功能已停用")
		}
		if err = tx.Table(rulesTable).Where("event=? AND enabled=1", event).Count(&configured).Error; err != nil {
			return err
		}
		if configured == 0 {
			return errors.New("该事件没有启用的积分奖励规则")
		}
		worker := NewService(tx, s.redis)
		worker.now = s.now
		for _, user := range users {
			var exists int64
			if err = tx.Table("customer.customers").Where("id=? AND deleted_at IS NULL AND status=1", user).Count(&exists).Error; err != nil {
				return err
			}
			if exists != 1 {
				return errors.New("积分客户不存在或已停用")
			}
			if _, err = worker.GrantByEvent(ctx, event, user, EventMeta{RefID: "admin-trigger:" + request, AmountCents: cents, Remark: notes}); err != nil {
				return err
			}
			affected++
		}
		return tx.Table("system.points_admin_actions").Create(map[string]any{"admin_id": actor, "request_id": request, "fingerprint": fingerprint, "action": "trigger", "notes": notes, "operator_ip": ip, "affected": affected}).Error
	})
	return affected, err
}
