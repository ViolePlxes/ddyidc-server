package cdnsell

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zeromicro/go-zero/rest"
)

func p2Routes(h *Handler) []rest.Route {
	return []rest.Route{
		{Method: http.MethodPost, Path: "/api/v1/cdnsell/domains", Handler: h.createDomain},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/domains", Handler: h.domains},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/domains/:id", Handler: h.domainDetail},
		{Method: http.MethodDelete, Path: "/api/v1/cdnsell/domains/:id", Handler: h.removeDomain},
		{Method: http.MethodPost, Path: "/api/v1/cdnsell/domains/:id/ownership-verify", Handler: h.startOwnership},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/domains/:id/ownership-status", Handler: h.ownershipStatus},
		{Method: http.MethodPost, Path: "/api/v1/cdnsell/domains/:id/enable", Handler: h.enableDomain},
		{Method: http.MethodPost, Path: "/api/v1/cdnsell/domains/:id/disable", Handler: h.disableDomain},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/domains/:id/sync", Handler: h.syncDomain},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/domains/:id/config", Handler: h.domainConfig},
		{Method: http.MethodPost, Path: "/api/v1/cdnsell/domains/:id/config", Handler: h.saveDomainConfig},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/domains/:id/config-tasks", Handler: h.configTasks},
		{Method: http.MethodPost, Path: "/api/v1/cdnsell/certs", Handler: h.uploadCertificate},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/certs", Handler: h.certificates},
		{Method: http.MethodPost, Path: "/api/v1/cdnsell/certs/:id/deploy", Handler: h.deployCertificate},
		{Method: http.MethodDelete, Path: "/api/v1/cdnsell/certs/:id", Handler: h.deleteCertificate},
		{Method: http.MethodPost, Path: "/api/v1/cdnsell/refresh", Handler: h.submitRefresh},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/refresh", Handler: h.refreshTasks},
		{Method: http.MethodGet, Path: "/api/v1/cdnsell/log-download", Handler: h.logDownloads},
		{Method: http.MethodPost, Path: "/api/v1/cdnsell/ip-check", Handler: h.ipCheck},
		{Method: http.MethodGet, Path: "/admin/v1/cdnsell/domains", Handler: h.adminDomains},
		{Method: http.MethodPost, Path: "/admin/v1/cdnsell/domains/:id/sync", Handler: h.adminSyncDomain},
		{Method: http.MethodGet, Path: "/admin/v1/cdnsell/domains/:id/config", Handler: h.adminDomainConfig},
		{Method: http.MethodPost, Path: "/admin/v1/cdnsell/domains/:id/config", Handler: h.adminSaveDomainConfig},
		{Method: http.MethodGet, Path: "/admin/v1/cdnsell/domains/:id/config-tasks", Handler: h.adminConfigTasks},
	}
}

func (h *Handler) createDomain(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	var input DomainInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.CreateDomain(r.Context(), userID, input)
	result(w, value, err)
}
func (h *Handler) domains(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	value, err := h.service.Domains(r.Context(), userID)
	result(w, value, err)
}
func (h *Handler) domainDetail(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.DomainDetail(r.Context(), userID, id, false)
	result(w, value, err)
}
func (h *Handler) removeDomain(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.DeleteDomain(r.Context(), userID, id)
	result(w, value, err)
}
func (h *Handler) startOwnership(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	var input struct {
		Method string `json:"method"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.StartOwnershipVerification(r.Context(), userID, id, input.Method)
	result(w, value, err)
}
func (h *Handler) ownershipStatus(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.OwnershipStatus(r.Context(), userID, id)
	result(w, value, err)
}
func (h *Handler) changeDomainState(w http.ResponseWriter, r *http.Request, enabled bool) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.SetDomainState(r.Context(), userID, id, enabled)
	result(w, value, err)
}
func (h *Handler) enableDomain(w http.ResponseWriter, r *http.Request) {
	h.changeDomainState(w, r, true)
}
func (h *Handler) disableDomain(w http.ResponseWriter, r *http.Request) {
	h.changeDomainState(w, r, false)
}
func (h *Handler) syncDomain(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.SyncDomain(r.Context(), userID, id, false)
	result(w, value, err)
}
func (h *Handler) domainConfig(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.DomainConfig(r.Context(), userID, id, r.URL.Query().Get("module"), false)
	result(w, value, err)
}
func (h *Handler) saveDomainConfig(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	var input struct {
		Module string         `json:"module"`
		Config map[string]any `json:"config"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.SaveDomainConfig(r.Context(), userID, id, input.Module, "member", input.Config, false)
	result(w, value, err)
}
func (h *Handler) configTasks(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.ConfigTasks(r.Context(), userID, id, false)
	result(w, value, err)
}

func (h *Handler) uploadCertificate(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	var input struct {
		DomainID int64  `json:"domainId,string"`
		Name     string `json:"name"`
		Cert     string `json:"cert"`
		Key      string `json:"key"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.UploadCertificate(r.Context(), userID, input.DomainID, input.Name, input.Cert, input.Key)
	result(w, value, err)
}
func (h *Handler) certificates(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	value, err := h.service.Certificates(r.Context(), userID)
	result(w, value, err)
}
func (h *Handler) deployCertificate(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.DeployCertificate(r.Context(), userID, id)
	result(w, value, err)
}
func (h *Handler) deleteCertificate(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	result(w, map[string]bool{"deleted": true}, h.service.DeleteCertificate(r.Context(), userID, id))
}

func splitURLs(value string) []string {
	lines := strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == '\r' })
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
func (h *Handler) submitRefresh(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	var input struct {
		Type    string   `json:"type"`
		URLs    []string `json:"urls"`
		URLText string   `json:"urlText"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.URLs) == 0 {
		input.URLs = splitURLs(input.URLText)
	}
	value, err := h.service.SubmitRefresh(r.Context(), userID, input.Type, input.URLs)
	result(w, value, err)
}
func (h *Handler) refreshTasks(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	value, err := h.service.RefreshTasks(r.Context(), userID)
	result(w, value, err)
}
func parseTimeQuery(value string) (time.Time, error) { return time.Parse(time.RFC3339, value) }
func (h *Handler) logDownloads(w http.ResponseWriter, r *http.Request) {
	userID, yes := h.user(w, r)
	if !yes {
		return
	}
	domainID, yes := queryID(w, r, "domainId")
	if !yes {
		return
	}
	start, err := parseTimeQuery(r.URL.Query().Get("start"))
	if err != nil {
		message(w, 400, "开始时间格式无效")
		return
	}
	end, err := parseTimeQuery(r.URL.Query().Get("end"))
	if err != nil {
		message(w, 400, "结束时间格式无效")
		return
	}
	value, err := h.service.LogDownloads(r.Context(), userID, domainID, start, end)
	result(w, value, err)
}
func queryID(w http.ResponseWriter, r *http.Request, key string) (int64, bool) {
	id, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	if err != nil || id <= 0 {
		message(w, 400, "ID 无效")
		return 0, false
	}
	return id, true
}
func (h *Handler) ipCheck(w http.ResponseWriter, r *http.Request) {
	if _, yes := h.user(w, r); !yes {
		return
	}
	var input struct {
		IP string `json:"ip"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.CheckIP(r.Context(), input.IP)
	result(w, value, err)
}

func (h *Handler) adminDomains(w http.ResponseWriter, r *http.Request) {
	if !h.admin(w, r) {
		return
	}
	value, err := h.service.AdminDomains(r.Context(), r.URL.Query().Get("keyword"))
	result(w, value, err)
}
func (h *Handler) adminSyncDomain(w http.ResponseWriter, r *http.Request) {
	adminID, yes := h.claims(w, r, "admin")
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.SyncDomain(r.Context(), adminID, id, true)
	result(w, value, err)
}
func (h *Handler) adminDomainConfig(w http.ResponseWriter, r *http.Request) {
	adminID, yes := h.claims(w, r, "admin")
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.DomainConfig(r.Context(), adminID, id, r.URL.Query().Get("module"), true)
	result(w, value, err)
}
func (h *Handler) adminSaveDomainConfig(w http.ResponseWriter, r *http.Request) {
	adminID, yes := h.claims(w, r, "admin")
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	var input struct {
		Module string         `json:"module"`
		Config map[string]any `json:"config"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.SaveDomainConfig(r.Context(), adminID, id, input.Module, "admin", input.Config, true)
	result(w, value, err)
}
func (h *Handler) adminConfigTasks(w http.ResponseWriter, r *http.Request) {
	adminID, yes := h.claims(w, r, "admin")
	if !yes {
		return
	}
	id, yes := pathID(w, r)
	if !yes {
		return
	}
	value, err := h.service.ConfigTasks(r.Context(), adminID, id, true)
	result(w, value, err)
}
