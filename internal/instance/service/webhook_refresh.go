package instance

import (
	"context"
	"ddyidc/internal/shared/tasks"
	"errors"
)

func (s *Service) RefreshWebhook(ctx context.Context, p tasks.RefreshPayload) error {
	host, err := s.owned(ctx, p.CustomerID, p.BusinessID)
	if err != nil {
		return err
	}
	if host.UpstreamProviderID == nil || *host.UpstreamProviderID != p.SupplierID || host.UpstreamInstanceID != p.RemoteID || host.OwnershipVersion != p.OwnershipVersion {
		return errors.New("回调归属已变化，未读取或覆盖实例状态")
	}
	state, err := s.Status(ctx, p.CustomerID, p.BusinessID)
	if err != nil {
		return err
	}
	if state.Warning != "" {
		return errors.New("回调已接收，实时状态未确认，请手动刷新")
	}
	return nil
}
