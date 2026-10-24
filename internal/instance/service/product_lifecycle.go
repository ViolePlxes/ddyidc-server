package instance

import (
	"context"
	balance "ddyidc/internal/billing/balance"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/notification"
	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/snowflake"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"time"
)

// One durable identity per ownership, original expiry and kind. Auto-renew never
// replays an uncertain payment; paid-order fulfillment owns its existing retries.
type ProductLifecycle struct {
	InstanceID       int64     `gorm:"primaryKey"`
	OwnershipVersion int64     `gorm:"primaryKey"`
	BaseExpiry       time.Time `gorm:"primaryKey"`
	Kind             string    `gorm:"primaryKey"`
	State            string
	Attempts         int
	NextAttemptAt    time.Time
	LastError        string
	UpdatedAt        time.Time
}

func (ProductLifecycle) TableName() string { return "instance.instance_product_lifecycle" }

// Keep slow expiry reconciliation independent of the time-sensitive renewal scan.
func (s *Service) StartExpirySyncMaintenance(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = distsync.Work(ctx, func(call context.Context) error {
				call, cancel := context.WithTimeout(call, 5*time.Minute)
				defer cancel()
				if s.db != nil {
					s.expirySyncTick(call)
				}
				return nil
			})
		}
	}
}

func lifecycleIdentity(h dom.Host, kind string) ProductLifecycle {
	return ProductLifecycle{InstanceID: h.ID, OwnershipVersion: h.OwnershipVersion, BaseExpiry: *h.ExpiresAt, Kind: kind}
}

func (s *Service) recordLifecycle(ctx context.Context, h dom.Host, kind, state, message string) error {
	if h.ExpiresAt == nil {
		return errors.New("缺少产品到期事件")
	}
	key := lifecycleIdentity(h, kind)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		fields := map[string]any{"state": state, "last_error": message, "updated_at": now}
		if state == "insufficient" {
			fields["next_attempt_at"] = now.Add(15 * time.Minute)
		}
		changed := tx.Model(&ProductLifecycle{}).Where(&key).Where("state='processing'").Updates(fields)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return nil
		}
		actor := uint8(3)
		status := uint8(2)
		if state == "failed" || state == "retry" || state == "uncertain" || state == "insufficient" {
			status = 3
		}
		if err := tx.Create(&dom.Event{InstanceID: h.ID, EventType: kind, OperatorType: actor, Status: status, ErrorMessage: message, UpstreamRequest: `{}`, UpstreamResponse: `{}`, CreatedAt: now, UpdatedAt: now, ProcessedAt: &now}).Error; err != nil {
			return err
		}
		if kind != "auto_renew_attempt" {
			return nil
		}
		return notification.EnqueueProductLifecycle(tx, notification.Event{Key: fmt.Sprintf("product:%d:%d:%d:%s", h.ID, h.OwnershipVersion, h.ExpiresAt.UnixMilli(), state), RecipientID: h.CustomerID, Type: "billing", Scene: "product_lifecycle", Title: "自动续费处理结果", Summary: message, Content: message, BusinessType: "instance", BusinessID: fmt.Sprint(h.ID), OwnershipVersion: h.OwnershipVersion})
	})
}

func (s *Service) autoRenewOnce(ctx context.Context, h dom.Host, cycle string) {
	key := lifecycleIdentity(h, "auto_renew_attempt")
	key.State = "processing"
	key.Attempts = 1
	key.NextAttemptAt = time.Now().Add(10 * time.Minute)
	key.UpdatedAt = time.Now()
	claim := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&key)
	if claim.Error != nil {
		log.Print("AUTO_RENEW claim persistence failed")
		return
	}
	if claim.RowsAffected != 1 {
		claimed := false
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var previous ProductLifecycle
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=? AND ownership_version=? AND base_expiry=? AND kind=?", h.ID, h.OwnershipVersion, *h.ExpiresAt, key.Kind).Take(&previous).Error; err != nil {
				return err
			}
			if previous.State != "insufficient" || previous.NextAttemptAt.After(time.Now()) || !h.ExpiresAt.After(time.Now()) {
				return nil
			}
			if err := tx.Model(&previous).Updates(map[string]any{"state": "processing", "attempts": previous.Attempts + 1, "next_attempt_at": time.Now().Add(10 * time.Minute), "updated_at": time.Now()}).Error; err != nil {
				return err
			}
			claimed = true
			return nil
		})
		if err != nil {
			log.Print("AUTO_RENEW retry claim failed")
			return
		}
		if !claimed {
			return
		}
	}
	ctx = context.WithValue(ctx, autoRenewPaymentKey{}, true)
	options, err := s.RenewalOptions(ctx, h.CustomerID, h.ID)
	var selected *RenewalOption
	if err == nil {
		for i := range options {
			if options[i].Cycle == cycle {
				selected = &options[i]
				break
			}
		}
	}
	if err == nil {
		if selected == nil {
			err = errors.New("当前续费周期不可用")
		} else {
			err = s.refreshAutoRenewalQuote(ctx, h, *selected)
		}
	}
	var o *RenewalOrder
	if err == nil {
		provider, warning := s.providerContext(ctx, &h)
		if warning != "" {
			err = errors.New("服务配置不可用")
		} else {
			real, e := provider.GetInstanceDetail(parseHostID(h.UpstreamInstanceID))
			if e != nil || real == nil || real.ID != h.UpstreamInstanceID || real.ExpiresAt == nil || !real.ExpiresAt.Equal(*h.ExpiresAt) || !real.ExpiresAt.After(time.Now()) || real.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
				err = errors.New("真实到期时间不在自动续费窗口")
			}
		}
	}
	if err == nil {
		o, err = s.CreateRenewalIntent(ctx, h.CustomerID, h.ID, cycle, RenewalIntent{QuoteRevision: selected.QuoteRevision})
	}
	message := "自动续费失败，未完成扣款，请检查余额并及时手动续费。"
	if err == nil {
		err = s.PayRenewalBalance(ctx, h.CustomerID, h.ID, parseHostID(o.OrderID), "system_job")
		if err == nil {
			if e := s.recordLifecycle(ctx, h, "auto_renew_attempt", "paid", "自动续费订单余额支付已受理，请以到期时间更新为准。"); e != nil {
				log.Print("AUTO_RENEW result persistence failed")
			}
			notification.Wake()
			return
		}
	}
	state := "failed"
	if errors.Is(err, balance.ErrInsufficient) {
		state = "insufficient"
		message = "账户余额不足，未扣费、未续费，请及时充值。到期前将每15分钟重试，避免到期停机。"
	}
	// Never copy provider payloads or credentials into customer notifications.
	if err != nil {
		log.Printf("AUTO_RENEW failed instance=%d; inspect order and balance audit", h.ID)
	}
	if e := s.recordLifecycle(ctx, h, "auto_renew_attempt", state, message); e != nil {
		log.Print("AUTO_RENEW failure notification persistence failed")
	}
	notification.Wake()
}

func (s *Service) reconcileAutoRenewAttempts(ctx context.Context) {
	var rows []ProductLifecycle
	if err := s.db.WithContext(ctx).Where("kind='auto_renew_attempt' AND state='processing' AND next_attempt_at<=?", time.Now()).Limit(50).Find(&rows).Error; err != nil {
		log.Print("AUTO_RENEW reconciliation scan failed")
		return
	}
	for _, row := range rows {
		var h dom.Host
		if err := s.db.WithContext(ctx).Where("id=? AND ownership_version=?", row.InstanceID, row.OwnershipVersion).Take(&h).Error; err != nil {
			continue
		}
		h.ExpiresAt = &row.BaseExpiry
		if err := s.recordLifecycle(ctx, h, "auto_renew_attempt", "uncertain", "自动续费处理结果未确认，系统不会重复扣费。请核对续费订单或联系客服。"); err != nil {
			log.Print("AUTO_RENEW reconciliation notification failed")
		}
	}
	notification.Wake()
}

func (s *Service) expirySyncTick(ctx context.Context) {
	var hosts []dom.Host
	now := time.Now()
	if err := s.db.WithContext(ctx).Table("instance.instances AS i").Select("i.*").Where("i.deleted_at IS NULL AND i.status IN (3,4,5) AND i.expires_at<=? AND i.upstream_instance_id<>''", now).Where("NOT EXISTS (SELECT 1 FROM instance.instance_product_lifecycle l WHERE l.instance_id=i.id AND l.ownership_version=i.ownership_version AND l.base_expiry=i.expires_at AND l.kind='expiry_sync' AND (l.state='completed' OR l.next_attempt_at>?))", now).Order("i.expires_at,i.id").Limit(100).Find(&hosts).Error; err != nil {
		log.Print("EXPIRY_SYNC candidate scan failed")
		return
	}
	for _, h := range hosts {
		if ctx.Err() != nil {
			return
		}
		key := lifecycleIdentity(h, "expiry_sync")
		key.State = "pending"
		key.NextAttemptAt = now
		key.UpdatedAt = now
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&key).Error; err != nil {
			log.Print("EXPIRY_SYNC event persistence failed")
			continue
		}
		leaseAttempt := 0
		claimErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var job ProductLifecycle
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=? AND ownership_version=? AND base_expiry=? AND kind=?", h.ID, h.OwnershipVersion, *h.ExpiresAt, key.Kind).Take(&job).Error; err != nil {
				return err
			}
			if job.State == "completed" || job.NextAttemptAt.After(time.Now()) {
				return nil
			}
			leaseAttempt = job.Attempts + 1
			return tx.Model(&job).Updates(map[string]any{"state": "processing", "attempts": leaseAttempt, "next_attempt_at": time.Now().Add(5 * time.Minute), "updated_at": time.Now()}).Error
		})
		if claimErr != nil || leaseAttempt == 0 {
			continue
		}
		err := s.withInstanceReadLock(ctx, h.ID, func() error {
			p, warning := s.providerContext(ctx, &h)
			if warning != "" {
				return errors.New("服务配置不可用")
			}
			real, err := p.GetInstanceDetail(parseHostID(h.UpstreamInstanceID))
			if err != nil || real == nil || real.ID != h.UpstreamInstanceID {
				return errors.New("到期状态核验失败")
			}
			fields, err := safeSyncFields(&h, real, time.Now())
			if err != nil {
				return err
			}
			power := unavailableLifecycle(real, h.UpstreamInstanceID)
			if power == "" {
				power = normalizePower(real.PowerStatus)
				if power == "" || power == "unknown" {
					raw, e := p.GetInstanceStatus(parseHostID(h.UpstreamInstanceID))
					if e != nil {
						return errors.New("到期电源状态核验失败")
					}
					power = normalizePower(raw)
				}
			}
			switch power {
			case "running", "stopped":
				fields["status"] = statusCode(power, h.Status)
				fields["power_status"] = power
			case "deleted":
				fields["status"] = uint8(6)
				fields["power_status"] = power
			case "expired":
				fields["status"] = uint8(5)
				fields["power_status"] = power
			case "suspended":
				state := h.Status
				if real.ExpiresAt != nil {
					if real.ExpiresAt.After(now) {
						if state == 5 {
							state = 3
						}
					} else {
						state = 5
					}
				}
				fields["status"] = state
				fields["power_status"] = power
			default:
				return errors.New("远端状态未知，保留本地状态")
			}
			// A remote Active response with the same past expiry is not a conclusive
			// expiry event. Keep checking with backoff rather than freezing this fact.
			if (power == "running" || power == "stopped") && (real.ExpiresAt == nil || !real.ExpiresAt.After(now)) {
				return errors.New("远端仍有效但到期时间未推进，等待再次核验")
			}
			return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				var current dom.Host
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", h.ID).Take(&current).Error; err != nil {
					return err
				}
				if !sameSyncReadVersion(current, h) {
					return errors.New("实例事实已变化，重新核验")
				}
				var lease ProductLifecycle
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("instance_id=? AND ownership_version=? AND base_expiry=? AND kind=? AND attempts=? AND state='processing'", h.ID, h.OwnershipVersion, *h.ExpiresAt, key.Kind, leaseAttempt).Take(&lease).Error; err != nil {
					return errors.New("到期同步租约已变化，忽略旧响应")
				}
				if err := tx.Model(&current).Updates(fields).Error; err != nil {
					return err
				}
				return tx.Model(&lease).Updates(map[string]any{"state": "completed", "last_error": "", "updated_at": time.Now()}).Error
			})
		})
		if err != nil {
			var current ProductLifecycle
			_ = s.db.WithContext(ctx).Where("instance_id=? AND ownership_version=? AND base_expiry=? AND kind=?", h.ID, h.OwnershipVersion, *h.ExpiresAt, key.Kind).Take(&current).Error
			delay := time.Duration(1<<min(current.Attempts, 6)) * time.Minute
			failed := s.db.WithContext(ctx).Model(&ProductLifecycle{}).Where("instance_id=? AND ownership_version=? AND base_expiry=? AND kind=? AND attempts=? AND state='processing'", h.ID, h.OwnershipVersion, *h.ExpiresAt, key.Kind, leaseAttempt).Updates(map[string]any{"state": "retry", "last_error": "到期状态同步失败，等待重试", "next_attempt_at": time.Now().Add(delay), "updated_at": time.Now()})
			if failed.Error != nil {
				log.Print("EXPIRY_SYNC retry persistence failed")
				continue
			}
			if failed.RowsAffected != 1 {
				continue
			}
			log.Printf("EXPIRY_SYNC retry instance=%d attempt=%d", h.ID, current.Attempts)
			_ = s.db.WithContext(ctx).Create(&dom.Event{ID: snowflake.GenID(), InstanceID: h.ID, EventType: "expiry_sync", OperatorType: 3, Status: 3, ErrorMessage: "到期状态同步失败，已安排重试", UpstreamRequest: `{}`, UpstreamResponse: `{}`, CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error
		}
	}
}
