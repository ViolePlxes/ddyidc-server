package points

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"sort"
	"strings"
)

func (s *Service) AdminGrantRequest(ctx context.Context, actor int64, ip, request string, users []int64, amount int64, notes string) (int, error) {
	if actor <= 0 || ip == "" || len(ip) > 64 || !exchangeRequestPattern.MatchString(request) || strings.TrimSpace(notes) == "" || len(notes) > 500 || len(users) == 0 || len(users) > 100 || amount == 0 || amount > 1000000 || amount < -1000000 {
		return 0, errors.New("人工调整需完整操作人、原因和请求标识，单次不超过100万积分或100个账户")
	}
	sort.Slice(users, func(i, j int) bool { return users[i] < users[j] })
	for i, id := range users {
		if id <= 0 || (i > 0 && users[i-1] == id) {
			return 0, errors.New("用户编号无效或重复")
		}
	}
	body, _ := json.Marshal(struct {
		Users  []int64
		Amount int64
		Notes  string
	}{users, amount, notes})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(body))
	affected := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		worker := NewService(tx, s.redis)
		worker.now = s.now
		for _, user := range users {
			var exists int64
			if err = tx.Table("customer.customers").Where("id=? AND deleted_at IS NULL", user).Count(&exists).Error; err != nil {
				return err
			}
			if exists != 1 {
				return errors.New("积分客户不存在")
			}
			ref := request
			if amount > 0 {
				err = worker.creditTx(ctx, tx, user, amount, "manual", EventManual, ref, notes)
			} else {
				_, err = worker.debitTx(ctx, tx, user, -amount, "manual", EventManual, ref, notes, false)
			}
			if err != nil {
				return err
			}
			affected++
		}
		return tx.Table("system.points_admin_actions").Create(map[string]any{"admin_id": actor, "request_id": request, "fingerprint": fingerprint, "action": "grant", "notes": notes, "operator_ip": ip, "affected": affected}).Error
	})
	return affected, err
}
