package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type ImportInput struct {
	ProductID          string `json:"productId"`
	UpstreamInstanceID string `json:"upstreamInstanceId"`
}

func (s *Service) ImportProducts(ctx context.Context) ([]repo.ImportProduct, error) {
	r, ok := s.repo.(repo.ImportRepository)
	if !ok {
		return nil, ErrUnsupported
	}
	return r.ImportProducts(ctx)
}
func (s *Service) Import(ctx context.Context, customerID int64, in ImportInput) (*dom.Host, error) {
	actor, adminID := actionActor(ctx, customerID)
	if actor != 2 || adminID <= 0 {
		return nil, ErrForbidden
	}
	productID, e := strconv.ParseInt(in.ProductID, 10, 64)
	if e != nil || productID <= 0 {
		return nil, errors.New("请选择已对接产品")
	}
	hostID, e := strconv.ParseInt(in.UpstreamInstanceID, 10, 64)
	if e != nil || hostID <= 0 {
		return nil, errors.New("请输入有效上游实例 ID，不是商品 ID")
	}
	r, ok := s.repo.(repo.ImportRepository)
	if !ok {
		return nil, ErrUnsupported
	}
	products, e := r.ImportProducts(ctx)
	if e != nil {
		return nil, e
	}
	var product *repo.ImportProduct
	for i := range products {
		if products[i].ID == productID {
			if product != nil {
				return nil, errors.New("产品存在多个对接关系，请先修正")
			}
			product = &products[i]
		}
	}
	if product == nil {
		return nil, errors.New("产品未对接或供应商已停用")
	}
	p, e := s.providers.ProviderFor(strconv.FormatInt(product.SupplierID, 10))
	if e != nil {
		return nil, errors.New("供应商配置不可用")
	}
	upstream, ok := p.(contracts.InstanceProvider)
	if !ok {
		return nil, ErrUnsupported
	}
	detail, e := upstream.GetInstanceDetail(hostID)
	if e != nil {
		return nil, fmt.Errorf("拉取上游实例失败：%w", e)
	}
	if detail == nil || detail.ID != strconv.FormatInt(hostID, 10) {
		return nil, errors.New("上游未返回匹配的实例身份，未保存")
	}
	if detail.ProductID != strconv.FormatInt(product.UpstreamProductID, 10) {
		return nil, errors.New("上游实例所属商品与所选产品不一致，未保存")
	}
	snapshot := snapshotFromUpstream(detail)
	raw, e := json.Marshal(snapshot)
	if e != nil {
		return nil, e
	}
	now := time.Now()
	id := snowflake.GenID()
	host := &dom.Host{ID: id, InstanceNo: "IMP" + strconv.FormatInt(id, 10), CustomerID: customerID, ProductID: product.ID, ProductName: product.Name, ModuleType: product.ModuleType, ConfigSnapshot: string(raw), Status: statusCode(detail.Status, 1), UpstreamProviderID: &product.SupplierID, UpstreamInstanceID: detail.ID, PrimaryIP: detail.PrimaryIP, SubnetMask: detail.SubnetMask, Gateway: detail.Gateway, OperatingSystem: detail.OperatingSystem, PowerStatus: detail.PowerStatus, ActivatedAt: detail.ActivatedAt, ExpiresAt: detail.ExpiresAt, LastSyncedAt: &now, CreatedAt: now, UpdatedAt: now}
	return r.SaveImported(ctx, host, adminID, *product)
}
func snapshotFromUpstream(detail *contracts.InstanceDetail) map[string]any {
	config := map[string]any{}
	for i, item := range detail.Config {
		config[fmt.Sprintf("upstream_%s_%d", item.ID, i)] = map[string]any{"value": item.Value, "field": item.Field, "displayName": item.Name, "displayValue": item.Value, "position": i}
	}
	return map[string]any{"configOptions": config, "hostname": detail.Hostname, "billingCycle": detail.BillingCycle, "source": "upstream_import"}
}
