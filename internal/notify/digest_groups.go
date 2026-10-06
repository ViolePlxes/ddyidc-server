package notify

import (
	"gorm.io/gorm"
	"net/http"
)

type digestTaskGroup struct {
	CustomerID int64        `json:"customerId,string"`
	NotifyDate string       `json:"notifyDate"`
	RealName   string       `json:"realName"`
	Channels   []DigestTask `json:"channels" gorm:"-"`
}

// Paginate customers' daily batches before fetching channels, so paired channels
// never split across pages. Filters select batches; every selected batch returns
// both channels to preserve the full delivery state.
func (h *adminHandler) digestTaskGroups(w http.ResponseWriter, r *http.Request, filtered *gorm.DB, page, size int) {
	db := h.manager.digest.db.WithContext(r.Context())
	grouped := filtered.Session(&gorm.Session{}).Select("customer_id,notify_date,MAX(created_at) latest,MAX(id) latest_id").Group("customer_id,notify_date")
	var total int64
	if err := db.Table("(?) batches", grouped).Count(&total).Error; err != nil {
		adminError(w, 500, 500, "读取汇总任务失败")
		return
	}
	groups := []digestTaskGroup{}
	if err := db.Table("(?) batches", grouped).Select("batches.customer_id,batches.notify_date,COALESCE((SELECT NULLIF(TRIM(cr.name),'') FROM customer.customer_realname cr WHERE cr.customer_id=batches.customer_id AND cr.status=2 ORDER BY cr.updated_at DESC,cr.id DESC LIMIT 1),'') real_name").Order("latest DESC,latest_id DESC").Limit(size).Offset((page - 1) * size).Scan(&groups).Error; err != nil {
		adminError(w, 500, 500, "读取汇总任务失败")
		return
	}
	if len(groups) > 0 {
		pairs := make([][]any, 0, len(groups))
		for _, group := range groups {
			pairs = append(pairs, []any{group.CustomerID, group.NotifyDate})
		}
		var channels []DigestTask
		if err := db.Where("(customer_id,notify_date) IN ?", pairs).Order("channel,id").Find(&channels).Error; err != nil {
			adminError(w, 500, 500, "读取通知渠道失败")
			return
		}
		for i := range groups {
			groups[i].Channels = []DigestTask{}
			for _, channel := range channels {
				if channel.CustomerID == groups[i].CustomerID && channel.NotifyDate == groups[i].NotifyDate {
					groups[i].Channels = append(groups[i].Channels, channel)
				}
			}
		}
	}
	adminOK(w, map[string]any{"items": groups, "total": total, "page": page, "pageSize": size})
}
