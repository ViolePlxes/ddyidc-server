package marketplace

import (
	"context"
	"ddyidc/pkg/clientip"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	paymenttypes "ddyidc/internal/payment/types"
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

type PaymentCreator interface {
	CreateMarketplace(ctx context.Context, customerID, orderID int64, channel, mode string) (*paymenttypes.CheckoutResponse, error)
}

type Handler struct {
	service  *Service
	jwt      *auth.Manager
	payments PaymentCreator
}

func RegisterHandlers(server *rest.Server, service *Service, jwt *auth.Manager, payments PaymentCreator) {
	h := &Handler{service: service, jwt: jwt, payments: payments}
	routes := []rest.Route{
		{Method: http.MethodGet, Path: "/api/v1/marketplace/status", Handler: h.status},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/listings", Handler: h.publicListings},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/listings/:id", Handler: h.publicListing},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/groups", Handler: h.groups},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/announcement", Handler: h.announcement},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/agreement", Handler: h.agreement},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/wants", Handler: h.publicWants},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/listings", Handler: h.createListing},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/listing/products", Handler: h.listingProducts},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/my/listings", Handler: h.myListings},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/listings/:id/off", Handler: h.offListing},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/listings/:id/price", Handler: h.reprice},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/listings/:id/favorite", Handler: h.favorite},
		{Method: http.MethodDelete, Path: "/api/v1/marketplace/listings/:id/favorite", Handler: h.unfavorite},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/orders", Handler: h.createOrder},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/orders/my", Handler: h.myOrders},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/orders/:id/pay", Handler: h.payOrder},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/orders/:id/confirm", Handler: h.confirmReceipt},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/orders/:id/refund", Handler: h.refundOrder},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/wallet", Handler: h.wallet},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/flows", Handler: h.myFlows},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/withdrawals", Handler: h.myWithdrawals},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/withdrawals", Handler: h.withdraw},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/wants", Handler: h.createWant},
		{Method: http.MethodGet, Path: "/api/v1/marketplace/wants/my", Handler: h.myWants},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/wants/:id/offers", Handler: h.offerWant},
		{Method: http.MethodPost, Path: "/api/v1/marketplace/reports", Handler: h.report},
		{Method: http.MethodGet, Path: "/admin/v1/marketplace/settings", Handler: h.adminSettings},
		{Method: http.MethodPut, Path: "/admin/v1/marketplace/settings", Handler: h.saveSettings},
		{Method: http.MethodGet, Path: "/admin/v1/marketplace/listings", Handler: h.adminListings},
		{Method: http.MethodPost, Path: "/admin/v1/marketplace/listings/:id/audit", Handler: h.auditListing},
		{Method: http.MethodPost, Path: "/admin/v1/marketplace/listings/:id/off", Handler: h.adminOffListing},
		{Method: http.MethodPost, Path: "/admin/v1/marketplace/listings/:id/recommend", Handler: h.recommendListing},
		{Method: http.MethodGet, Path: "/admin/v1/marketplace/orders", Handler: h.adminOrders},
		{Method: http.MethodPost, Path: "/admin/v1/marketplace/orders/:id/refund", Handler: h.adminRefund},
		{Method: http.MethodGet, Path: "/admin/v1/marketplace/flows", Handler: h.adminFlows},
		{Method: http.MethodGet, Path: "/admin/v1/marketplace/withdrawals", Handler: h.adminWithdrawals},
		{Method: http.MethodPost, Path: "/admin/v1/marketplace/withdrawals/:id/audit", Handler: h.auditWithdrawal},
		{Method: http.MethodGet, Path: "/admin/v1/marketplace/reports", Handler: h.adminReports},
		{Method: http.MethodPost, Path: "/admin/v1/marketplace/reports/:id/handle", Handler: h.handleReport},
		{Method: http.MethodGet, Path: "/admin/v1/marketplace/dashboard", Handler: h.dashboard},
	}
	for i := range routes {
		routes[i].Handler = h.guard(routes[i].Handler)
	}
	server.AddRoutes(routes)
}

func (h *Handler) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		enabled, err := h.service.Enabled(r.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/admin/v1/marketplace/") || r.URL.Path == "/api/v1/marketplace/status" {
			next(w, r)
			return
		}
		existing := r.URL.Path == "/api/v1/marketplace/orders/my" || strings.HasPrefix(r.URL.Path, "/api/v1/marketplace/orders/") || r.URL.Path == "/api/v1/marketplace/my/listings" || r.URL.Path == "/api/v1/marketplace/wallet" || r.URL.Path == "/api/v1/marketplace/flows" || r.URL.Path == "/api/v1/marketplace/withdrawals" || strings.HasSuffix(r.URL.Path, "/off")
		if !enabled && !existing {
			writeError(w, http.StatusNotFound, "交易市场未开启")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/marketplace") && r.Method != http.MethodGet && !existing {
			settings, settingsErr := h.service.Settings(r.Context())
			if settingsErr != nil {
				writeServiceError(w, settingsErr)
				return
			}
			if settings.Maintenance {
				writeServiceError(w, ErrMaintenance)
				return
			}
		}
		next(w, r)
	}
}
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	enabled, err := h.service.Enabled(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	settings, err := h.service.Settings(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	stats, _ := h.service.Dashboard(r.Context())
	writeOK(w, map[string]any{"enabled": enabled, "maintenance": settings.Maintenance, "feeType": settings.FeeType, "feeRate": settings.FeeRate, "feeFixed": settings.FeeFixed, "feeBearer": settings.FeeBearer, "refundEnabled": settings.RefundEnabled, "onSale": stats["onSale"], "sold": stats["sold"], "wants": stats["wants"]})
}
func (h *Handler) publicListings(w http.ResponseWriter, r *http.Request) {
	p, z := pages(r)
	group, _ := strconv.ParseInt(r.URL.Query().Get("groupId"), 10, 64)
	rows, total, err := h.service.ListListings(r.Context(), ListingQuery{Keyword: r.URL.Query().Get("keyword"), Region: r.URL.Query().Get("region"), Sort: r.URL.Query().Get("sort"), Quick: r.URL.Query().Get("quick"), GroupID: group, Page: p, PageSize: z, Public: true})
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) publicListing(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	row, err := h.service.Listing(r.Context(), id, 0, true)
	writeResult(w, row, err)
}
func (h *Handler) groups(w http.ResponseWriter, r *http.Request) {
	row, err := h.service.Groups(r.Context())
	writeResult(w, row, err)
}
func (h *Handler) announcement(w http.ResponseWriter, r *http.Request) {
	row, err := h.service.Announcement(r.Context())
	writeResult(w, row, err)
}
func (h *Handler) agreement(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.Agreement(r.Context())
	writeResult(w, map[string]string{"agreement": value}, err)
}
func (h *Handler) publicWants(w http.ResponseWriter, r *http.Request) {
	p, z := pages(r)
	rows, total, err := h.service.ListWants(r.Context(), 0, false, p, z)
	writePage(w, rows, total, p, z, err)
}

func (h *Handler) createListing(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	var input struct {
		ProductID            string `json:"productId"`
		Title                string `json:"title"`
		Region               string `json:"region"`
		Price                string `json:"price"`
		OriginalRenewalPrice string `json:"originalRenewalPrice"`
		SellerNote           string `json:"sellerNote"`
		BackupConfirmed      bool   `json:"backupConfirmed"`
	}
	if !decode(w, r, &input) {
		return
	}
	productID, err := strconv.ParseInt(input.ProductID, 10, 64)
	if err != nil {
		writeError(w, 400, "产品ID无效")
		return
	}
	row, err := h.service.CreateListing(r.Context(), user, CreateListingInput{ProductID: productID, Title: input.Title, Region: input.Region, Price: input.Price, OriginalRenewalPrice: input.OriginalRenewalPrice, SellerNote: input.SellerNote, BackupConfirmed: input.BackupConfirmed})
	writeResult(w, row, err)
}
func (h *Handler) listingProducts(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	rows, err := h.service.ListingProducts(r.Context(), user, r.URL.Query().Get("keyword"))
	writeResult(w, rows, err)
}
func (h *Handler) myListings(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.ListListings(r.Context(), ListingQuery{Keyword: r.URL.Query().Get("keyword"), Status: r.URL.Query().Get("status"), ViewerID: user, Page: p, PageSize: z})
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) offListing(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.OffListing(r.Context(), user, id, false))
}
func (h *Handler) reprice(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Price string `json:"price"`
	}
	if !decode(w, r, &in) {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.Reprice(r.Context(), user, id, in.Price))
}
func (h *Handler) favorite(w http.ResponseWriter, r *http.Request)   { h.setFavorite(w, r, true) }
func (h *Handler) unfavorite(w http.ResponseWriter, r *http.Request) { h.setFavorite(w, r, false) }
func (h *Handler) setFavorite(w http.ResponseWriter, r *http.Request, on bool) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"favorite": on}, h.service.Favorite(r.Context(), user, id, on))
}

func (h *Handler) createOrder(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	var in struct {
		ListingID string `json:"listingId"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, err := strconv.ParseInt(in.ListingID, 10, 64)
	if err != nil {
		writeError(w, 400, "挂牌ID无效")
		return
	}
	row, err := h.service.CreateOrder(r.Context(), user, CreateOrderInput{ListingID: id, OperatorIP: requestIP(r)})
	writeResult(w, row, err)
}
func (h *Handler) myOrders(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.ListOrders(r.Context(), user, false, r.URL.Query().Get("status"), p, z)
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) payOrder(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Method string `json:"method"`
		Mode   string `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	method := strings.ToLower(strings.TrimSpace(in.Method))
	if method == "balance" {
		row, err := h.service.PayBalance(r.Context(), user, id)
		writeResult(w, row, err)
		return
	}
	if method != "alipay" && method != "wechat" {
		writeError(w, 400, "不支持的支付方式")
		return
	}
	if h.payments == nil {
		writeError(w, 503, "支付服务未初始化")
		return
	}
	row, err := h.payments.CreateMarketplace(r.Context(), user, id, method, in.Mode)
	writeResult(w, row, err)
}
func (h *Handler) refundOrder(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	row, err := h.service.Refund(r.Context(), user, id, false)
	writeResult(w, row, err)
}
func (h *Handler) wallet(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	row, err := h.service.Wallet(r.Context(), user)
	writeResult(w, row, err)
}
func (h *Handler) myFlows(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.ListFlows(r.Context(), user, false, r.URL.Query().Get("type"), r.URL.Query().Get("direction"), p, z)
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) myWithdrawals(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.ListWithdrawals(r.Context(), user, false, r.URL.Query().Get("status"), p, z)
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) withdraw(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	var in struct {
		Amount    string `json:"amount"`
		Account   string `json:"account"`
		RequestID string `json:"requestId"`
	}
	if !decode(w, r, &in) {
		return
	}
	row, err := h.service.CreateWithdrawalWithAccount(r.Context(), user, in.Amount, in.Account, in.RequestID)
	writeResult(w, row, err)
}
func (h *Handler) createWant(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	var row Want
	if !decode(w, r, &row) {
		return
	}
	created, err := h.service.CreateWant(r.Context(), user, &row)
	writeResult(w, created, err)
}
func (h *Handler) myWants(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.ListWants(r.Context(), user, true, p, z)
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) offerWant(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		ListingID *string `json:"listingId"`
		Price     string  `json:"price"`
		Message   string  `json:"message"`
	}
	if !decode(w, r, &in) {
		return
	}
	var listingID *int64
	if in.ListingID != nil && *in.ListingID != "" {
		v, e := strconv.ParseInt(*in.ListingID, 10, 64)
		if e != nil {
			writeError(w, 400, "关联挂牌ID无效")
			return
		}
		listingID = &v
	}
	row, err := h.service.OfferWant(r.Context(), user, id, &WantOffer{ListingID: listingID, Price: in.Price, Message: in.Message})
	writeResult(w, row, err)
}
func (h *Handler) report(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	var in struct {
		ListingID string `json:"listingId"`
		Reason    string `json:"reason"`
		Detail    string `json:"detail"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, e := strconv.ParseInt(in.ListingID, 10, 64)
	if e != nil {
		writeError(w, 400, "挂牌ID无效")
		return
	}
	row, err := h.service.CreateReport(r.Context(), user, &Report{ListingID: id, Reason: in.Reason, Detail: in.Detail})
	writeResult(w, row, err)
}

func (h *Handler) adminSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	row, err := h.service.Settings(r.Context())
	writeResult(w, row, err)
}
func (h *Handler) saveSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	var row Settings
	if !decode(w, r, &row) {
		return
	}
	err := h.service.SaveSettings(r.Context(), &row)
	writeResult(w, &row, err)
}
func (h *Handler) adminListings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.ListListings(r.Context(), ListingQuery{Keyword: r.URL.Query().Get("keyword"), Region: r.URL.Query().Get("region"), Status: r.URL.Query().Get("status"), AuditStatus: r.URL.Query().Get("auditStatus"), Page: p, PageSize: z})
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) auditListing(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.claims(w, r, "admin")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Passed bool   `json:"passed"`
		Remark string `json:"remark"`
	}
	if !decode(w, r, &in) {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.AuditListing(r.Context(), admin, id, in.Passed, in.Remark))
}
func (h *Handler) adminOffListing(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.claims(w, r, "admin")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.OffListing(r.Context(), admin, id, true))
}
func (h *Handler) recommendListing(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Recommended bool `json:"recommended"`
	}
	if !decode(w, r, &in) {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.RecommendListing(r.Context(), id, in.Recommended))
}
func (h *Handler) adminOrders(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	p, z := pages(r)
	var records []int64
	if value := r.URL.Query().Get("record"); value != "" {
		id, e := strconv.ParseInt(value, 10, 64)
		if e != nil || id <= 0 {
			writeResult(w, nil, ErrInvalid)
			return
		}
		records = append(records, id)
	}
	rows, total, err := h.service.ListOrders(r.Context(), 0, true, r.URL.Query().Get("status"), p, z, records...)
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) adminRefund(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.claims(w, r, "admin")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	row, err := h.service.Refund(r.Context(), admin, id, true)
	writeResult(w, row, err)
}
func (h *Handler) adminFlows(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	p, z := pages(r)
	user, _ := strconv.ParseInt(r.URL.Query().Get("userId"), 10, 64)
	rows, total, err := h.service.ListFlows(r.Context(), user, true, r.URL.Query().Get("type"), r.URL.Query().Get("direction"), p, z)
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) adminWithdrawals(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.ListWithdrawals(r.Context(), 0, true, r.URL.Query().Get("status"), p, z)
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) auditWithdrawal(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.claims(w, r, "admin")
	if !ok {
		return
	}
	var super int64
	if h.service.db.WithContext(r.Context()).Table("system.admins").Where("id=? AND status=1 AND is_super=1", admin).Count(&super).Error != nil || super != 1 {
		writeError(w, 403, "仅超级管理员可审核提现")
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Approved          bool      `json:"approved"`
		Remark            string    `json:"remark"`
		RequestID         string    `json:"requestId"`
		ExpectedUpdatedAt time.Time `json:"expectedUpdatedAt"`
		ConfirmLarge      bool      `json:"confirmLarge"`
	}
	if !decode(w, r, &in) {
		return
	}
	row, err := h.service.ReviewWithdrawal(WithOperatorIP(r.Context(), clientip.FromRequest(r)), admin, id, in.Approved, in.Remark, in.RequestID, in.ExpectedUpdatedAt, in.ConfirmLarge)
	writeResult(w, row, err)
}
func (h *Handler) adminReports(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	p, z := pages(r)
	rows, total, err := h.service.ListReports(r.Context(), r.URL.Query().Get("status"), p, z)
	writePage(w, rows, total, p, z, err)
}
func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.claims(w, r, "admin")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if !decode(w, r, &in) {
		return
	}
	writeResult(w, map[string]bool{"updated": true}, h.service.HandleReport(r.Context(), admin, id, in.Status))
}
func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.claims(w, r, "admin"); !ok {
		return
	}
	row, err := h.service.Dashboard(r.Context())
	writeResult(w, row, err)
}

func (h *Handler) claims(w http.ResponseWriter, r *http.Request, role string) (int64, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return 0, false
	}
	claims, err := h.jwt.ParseAccessToken(strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims == nil {
		writeError(w, http.StatusUnauthorized, "登录已失效，请重新登录")
		return 0, false
	}
	if claims.Role != role {
		writeError(w, http.StatusForbidden, "没有访问权限")
		return 0, false
	}
	return claims.UserID, true
}
func decode(w http.ResponseWriter, r *http.Request, to any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(to); err != nil {
		writeError(w, 400, "请求参数格式错误")
		return false
	}
	return true
}
func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(strings.Trim(pathvar.Vars(r)[name], "/"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, "路径参数错误")
		return 0, false
	}
	return id, true
}
func pages(r *http.Request) (int, int) {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	z, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	return normalizePage(p, z)
}
func writeOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}
func writeResult(w http.ResponseWriter, data any, err error) {
	if err == nil {
		writeOK(w, data)
		return
	}
	writeServiceError(w, err)
}
func writePage(w http.ResponseWriter, rows any, total int64, page, size int, err error) {
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeOK(w, map[string]any{"list": rows, "total": total, "page": page, "pageSize": size})
}
func writeServiceError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, ErrMaintenance):
		status = http.StatusServiceUnavailable
	}
	writeError(w, status, err.Error())
}
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "message": message})
}

func requestIP(r *http.Request) string { return clientip.FromRequest(r) }
func (h *Handler) confirmReceipt(w http.ResponseWriter, r *http.Request) {
	user, ok := h.claims(w, r, "customer")
	if !ok {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	row, err := h.service.ConfirmReceipt(r.Context(), user, id)
	writeResult(w, row, err)
}
