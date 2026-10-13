package cdnsell

import (
	"context"
	"ddyidc/internal/cdnsell/provider"
	"errors"
	"net/http"
	"strconv"
)

func (s *Service) CloudCertificates(ctx context.Context, user int64, admin bool, id int64) ([]provider.CloudCertificate, error) {
	enabled, e := s.Enabled(ctx)
	if e != nil || !enabled {
		return nil, errors.New("CDN服务未开启")
	}
	var rows []DomainRecord
	if e = s.db.WithContext(ctx).Table(domainsTable).Where("id=?", id).Find(&rows).Error; e != nil {
		return nil, errors.New("域名查询失败")
	}
	rows, e = statisticsDomains(rows, []int64{id}, user, admin)
	if e != nil {
		return nil, e
	}
	reader, ok := s.cloud.(provider.CertificateReader)
	if !ok {
		return nil, provider.ErrCapabilityNotOpen
	}
	config, a, e := s.accountSecret(ctx, rows[0].AccountID)
	if e != nil || !config.Status {
		return nil, errors.New("服务账户不可用")
	}
	if e = s.waitCloudGap(ctx); e != nil {
		return nil, e
	}
	return reader.GetCertificates(ctx, a, rows[0].Domain)
}
func (h *Handler) cloudCertificates(w http.ResponseWriter, r *http.Request) {
	user, ok := h.user(w, r)
	if ok {
		h.cloudCertificatesQuery(w, r, user, false)
	}
}
func (h *Handler) adminCloudCertificates(w http.ResponseWriter, r *http.Request) {
	if h.admin(w, r) {
		h.cloudCertificatesQuery(w, r, 0, true)
	}
}
func (h *Handler) cloudCertificatesQuery(w http.ResponseWriter, r *http.Request, user int64, admin bool) {
	w.Header().Set("Cache-Control", "no-store")
	id, e := strconv.ParseInt(r.URL.Query().Get("domainId"), 10, 64)
	if e != nil || id <= 0 {
		message(w, 400, "请选择有效域名")
		return
	}
	rows, e := h.service.CloudCertificates(r.Context(), user, admin, id)
	if errors.Is(e, ErrForbidden) {
		message(w, 403, "无权查询所选域名")
		return
	}
	result(w, rows, e)
}
func (h *Handler) adminIPCheck(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	var input struct {
		IP string `json:"ip"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, e := h.service.CheckIP(r.Context(), input.IP)
	result(w, value, e)
}
