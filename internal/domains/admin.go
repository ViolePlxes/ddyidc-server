package domains

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/clientip"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AdminAsset struct {
	ID           int64      `json:"id,string"`
	CustomerID   int64      `json:"customerId,string"`
	CustomerName string     `json:"customerName"`
	Domain       string     `json:"domain"`
	Status       string     `json:"status"`
	ExpiresAt    *time.Time `json:"expiresAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}
type AdminAssetFilter struct {
	Keyword, Status, Expiry string
	Page, PageSize          int
}
type AdminSummary struct {
	Total        int64 `json:"total"`
	Active       int64 `json:"active"`
	Pending      int64 `json:"pending"`
	Expired      int64 `json:"expired"`
	Expiring     int64 `json:"expiring"`
	ReviewOrders int64 `json:"reviewOrders"`
}
type AdminAssetPage struct {
	Items    []AdminAsset `json:"items"`
	Total    int64        `json:"total"`
	Page     int          `json:"page"`
	PageSize int          `json:"pageSize"`
	Summary  AdminSummary `json:"summary"`
}
type AdminAssetDetail struct {
	Asset      AdminAsset    `json:"asset"`
	Orders     []DomainOrder `json:"orders"`
	Operations []Operation   `json:"operations"`
}

func adminAssetQuery(db *gorm.DB) *gorm.DB {
	return db.Table("system.domain_assets a").Joins("LEFT JOIN customer.customers c ON c.id=a.customer_id")
}

const adminAssetColumns = "a.id,a.customer_id,CASE WHEN c.username REGEXP '^v10[_-][0-9]+$' THEN CAST(c.id AS CHAR) ELSE COALESCE(c.username,'已删除客户') END AS customer_name,a.domain,a.status,a.expires_at,a.updated_at"

func (s *Service) AdminAssets(ctx context.Context, f AdminAssetFilter) (*AdminAssetPage, error) {
	if f.Page < 1 || f.Page > 100000 || f.PageSize < 1 || f.PageSize > 100 || len(f.Keyword) > 253 {
		return nil, errors.New("筛选或分页参数无效")
	}
	if f.Status != "" && f.Status != "active" && f.Status != "pending" {
		return nil, errors.New("域名状态筛选无效")
	}
	if f.Expiry != "" && f.Expiry != "expired" && f.Expiry != "expiring" {
		return nil, errors.New("到期筛选无效")
	}
	result := &AdminAssetPage{Items: []AdminAsset{}, Page: f.Page, PageSize: f.PageSize}
	now := s.now()
	soon := now.Add(30 * 24 * time.Hour)
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := adminAssetQuery(tx)
		if keyword := strings.TrimSpace(f.Keyword); keyword != "" {
			q = q.Where("(LOCATE(?,a.domain)>0 OR LOCATE(?,c.username)>0 OR CAST(a.customer_id AS CHAR)=?)", keyword, keyword, keyword)
		}
		if f.Status != "" {
			q = q.Where("a.status=?", f.Status)
		}
		if f.Expiry == "expired" {
			q = q.Where("a.expires_at<=?", now)
		} else if f.Expiry == "expiring" {
			q = q.Where("a.expires_at>? AND a.expires_at<=?", now, soon)
		}
		if e := q.Count(&result.Total).Error; e != nil {
			return e
		}
		if e := q.Select(adminAssetColumns).Order("a.id DESC").Limit(f.PageSize).Offset((f.Page - 1) * f.PageSize).Scan(&result.Items).Error; e != nil {
			return e
		}
		for _, v := range []struct {
			sql    string
			args   []any
			target *int64
		}{
			{"1=1", nil, &result.Summary.Total}, {"status='active'", nil, &result.Summary.Active}, {"status='pending'", nil, &result.Summary.Pending},
			{"expires_at<=?", []any{now}, &result.Summary.Expired}, {"expires_at>? AND expires_at<=?", []any{now, soon}, &result.Summary.Expiring},
		} {
			if e := tx.Model(&Asset{}).Where(v.sql, v.args...).Count(v.target).Error; e != nil {
				return e
			}
		}
		return tx.Model(&DomainOrder{}).Where("status IN ('unknown','review','failed')").Count(&result.Summary.ReviewOrders).Error
	})
	return result, e
}
func (s *Service) AdminAssetDetail(ctx context.Context, id int64) (*AdminAssetDetail, error) {
	if id <= 0 {
		return nil, ErrOwnership
	}
	result := &AdminAssetDetail{Orders: []DomainOrder{}, Operations: []Operation{}}
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows := adminAssetQuery(tx).Select(adminAssetColumns).Where("a.id=?", id).Scan(&result.Asset)
		if rows.Error != nil {
			return rows.Error
		}
		if rows.RowsAffected != 1 {
			return ErrOwnership
		}
		if e := tx.Where("customer_id=? AND domain=?", result.Asset.CustomerID, result.Asset.Domain).Order("created_at DESC,id DESC").Limit(50).Find(&result.Orders).Error; e != nil {
			return e
		}
		return tx.Select("id,action,domain,status,error_code,created_at,updated_at").Where("customer_id=? AND domain=?", result.Asset.CustomerID, result.Asset.Domain).Order("created_at DESC,id DESC").Limit(50).Find(&result.Operations).Error
	})
	return result, e
}

// Administrator refresh reads registrar metadata only. Pending ownership claims
// cannot be promoted by a metadata refresh, and no purchase is ever retried.
func (s *Service) AdminRefreshAsset(ctx context.Context, admin, id int64, ip string) error {
	if admin <= 0 || id <= 0 {
		return ErrOwnership
	}
	var before Asset
	if e := s.db.WithContext(ctx).Take(&before, "id=?", id).Error; e != nil {
		return ErrOwnership
	}
	if before.Status != "active" {
		return errors.New("域名归属尚未确认，请先核对原订单")
	}
	cfg, e := s.ready(ctx)
	if e != nil {
		return e
	}
	info, e := s.providerData(ctx, cfg, "4.8", map[string]string{"domain": before.Domain})
	if e != nil {
		return e
	}
	if stringValue(info["domain"]) != before.Domain {
		return ErrOwnership
	}
	expiry, e := domainTime(stringValue(info["rexpiredate"]))
	if e != nil {
		return e
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current Asset
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&current, "id=?", id).Error; e != nil {
			return e
		}
		if current.CustomerID != before.CustomerID || current.Domain != before.Domain || current.Status != "active" || !current.UpdatedAt.Equal(before.UpdatedAt) {
			return errors.New("域名记录已变化，请刷新后重试")
		}
		if e := tx.Model(&current).Updates(map[string]any{"expires_at": expiry, "updated_at": s.now()}).Error; e != nil {
			return e
		}
		return tx.Table("system.domain_admin_audits").Create(map[string]any{"id": snowflake.GenID(), "admin_id": admin, "operator_ip": ip, "action": "refresh_domain", "notes": fmt.Sprintf("asset_id=%d domain=%s customer_id=%d", id, current.Domain, current.CustomerID), "created_at": s.now()}).Error
	})
}
func (h *Handler) adminAssets(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.actor(w, r, true); ok {
		q := r.URL.Query()
		page, size := 1, 20
		var e error
		if q.Get("page") != "" {
			page, e = strconv.Atoi(q.Get("page"))
			if e != nil {
				reply(w, 400, nil, "页码无效")
				return
			}
		}
		if q.Get("pageSize") != "" {
			size, e = strconv.Atoi(q.Get("pageSize"))
			if e != nil {
				reply(w, 400, nil, "每页条数无效")
				return
			}
		}
		v, e := h.s.AdminAssets(r.Context(), AdminAssetFilter{q.Get("keyword"), q.Get("status"), q.Get("expiry"), page, size})
		finish(w, v, e)
	}
}
func (h *Handler) adminAsset(w http.ResponseWriter, r *http.Request) {
	if admin, ok := h.actor(w, r, true); ok {
		if r.Method == http.MethodGet {
			id, e := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
			if e != nil {
				reply(w, 400, nil, "域名编号无效")
				return
			}
			v, e := h.s.AdminAssetDetail(r.Context(), id)
			finish(w, v, e)
			return
		}
		var in struct {
			ID        string `json:"id"`
			Confirmed bool   `json:"confirmed"`
		}
		if !readBody(w, r, &in) {
			return
		}
		id, e := strconv.ParseInt(in.ID, 10, 64)
		if e != nil || !in.Confirmed {
			reply(w, 400, nil, "请确认核对域名信息")
			return
		}
		e = h.s.AdminRefreshAsset(r.Context(), admin, id, clientip.FromRequest(r))
		finish(w, map[string]bool{"updated": e == nil}, e)
	}
}
