package instance

import (
	"context"
	"errors"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
)

func (s *Service) snapshotProvider(ctx context.Context, customerID, id int64) (contracts.SnapshotProvider, int64, error) {
	if s.db == nil {
		return nil, 0, ErrUnsupported
	}
	host, err := s.owned(ctx, customerID, id)
	if err != nil {
		return nil, 0, err
	}
	if host.Status == 6 {
		return nil, 0, ErrUnsupported
	}
	p, warning := s.providerContext(ctx, host)
	if warning != "" {
		return nil, 0, ErrUnsupported
	}
	snapshots, ok := p.(contracts.SnapshotProvider)
	if !ok {
		return nil, 0, ErrUnsupported
	}
	return snapshots, parseHostID(host.UpstreamInstanceID), nil
}

func (s *Service) Snapshots(ctx context.Context, customerID, id int64) ([]contracts.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	provider, hostID, err := s.snapshotProvider(ctx, customerID, id)
	if err != nil {
		return nil, err
	}
	return provider.ListSnapshots(hostID)
}

func (s *Service) CreateSnapshot(ctx context.Context, customerID, id int64, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if name == "" {
		return errors.New("请填写快照备注名")
	}
	provider, hostID, err := s.snapshotProvider(ctx, customerID, id)
	if err != nil {
		return err
	}
	return provider.CreateSnapshot(hostID, name)
}

func (s *Service) RestoreSnapshot(ctx context.Context, customerID, id int64, snapshotID string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if snapshotID == "" {
		return errors.New("快照编号无效")
	}
	provider, hostID, err := s.snapshotProvider(ctx, customerID, id)
	if err != nil {
		return err
	}
	return provider.RestoreSnapshot(hostID, snapshotID)
}

func (s *Service) DeleteSnapshot(ctx context.Context, customerID, id int64, snapshotID string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if snapshotID == "" {
		return errors.New("快照编号无效")
	}
	provider, hostID, err := s.snapshotProvider(ctx, customerID, id)
	if err != nil {
		return err
	}
	return provider.DeleteSnapshot(hostID, snapshotID)
}
