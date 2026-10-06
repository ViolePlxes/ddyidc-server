package notify

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// Move missed, unsubmitted batches to an available day without resetting per-cycle deduplication.
func (d *DigestService) rollover(ctx context.Context, now time.Time, settings DigestSettings) error {
	today := now.In(digestZone).Format("2006-01-02")
	var missed []DigestTask
	q := d.db.WithContext(ctx).Where("status='pending' AND notify_date<?", today)
	if !settings.AllCustomers {
		q = q.Where("customer_id=?", settings.CustomerID)
	}
	if e := q.Limit(100).Find(&missed).Error; e != nil {
		return e
	}
	for _, candidate := range missed {
		if e := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var task DigestTask
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, "id=?", candidate.ID).Error; e != nil {
				return e
			}
			if task.Status != "pending" || task.NotifyDate >= today {
				return nil
			}
			date := today
			due := now.Add(15 * time.Minute)
			for n := 0; n < 32; n++ {
				var target DigestTask
				e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id=? AND notify_date=? AND channel=?", task.CustomerID, date, task.Channel).Take(&target).Error
				if errors.Is(e, gorm.ErrRecordNotFound) {
					return tx.Model(&task).Updates(map[string]any{"notify_date": date, "scheduled_at": due, "updated_at": now, "version": gorm.Expr("version+1")}).Error
				}
				if e != nil {
					return e
				}
				if target.Status == "pending" {
					if e := tx.Model(&DigestItem{}).Where("task_id=?", task.ID).Update("task_id", target.ID).Error; e != nil {
						return e
					}
					updates := map[string]any{"count": gorm.Expr("count+?", task.Count), "updated_at": now, "version": gorm.Expr("version+1")}
					if task.EarliestExpiry != nil && (target.EarliestExpiry == nil || task.EarliestExpiry.Before(*target.EarliestExpiry)) {
						updates["earliest_expiry"] = task.EarliestExpiry
					}
					if due.Before(target.ScheduledAt) {
						updates["scheduled_at"] = due
					}
					if e := tx.Model(&target).Updates(updates).Error; e != nil {
						return e
					}
					return tx.Model(&task).Updates(map[string]any{"status": "canceled", "error": "未发送明细已合并到当前批次", "updated_at": now, "version": gorm.Expr("version+1")}).Error
				}
				local, _ := time.ParseInLocation("2006-01-02", date, digestZone)
				local = local.AddDate(0, 0, 1)
				date = local.Format("2006-01-02")
				due = local.Add(9 * time.Hour).UTC()
			}
			return errors.New("没有可用通知日期")
		}); e != nil {
			return e
		}
	}
	return nil
}
