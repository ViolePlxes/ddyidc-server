package ticket

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	repo "ddyidc/internal/ticket/repository"
	"github.com/google/uuid"
)

func (s *Service) ReserveUpload(ctx context.Context, u *repo.Upload) error {
	return s.repo.ReserveUpload(ctx, u)
}
func (s *Service) CompleteUpload(ctx context.Context, id, hash string, size int64) error {
	return s.repo.CompleteUpload(ctx, id, hash, size)
}
func (s *Service) DiscardUpload(ctx context.Context, id, root string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if _, err := uuid.Parse(id); err != nil {
		return
	}
	ok, err := s.repo.DiscardUpload(ctx, id)
	if err != nil || !ok {
		return
	}
	if err = os.Remove(filepath.Join(root, id)); err == nil || os.IsNotExist(err) {
		_ = s.repo.FinishDelete(ctx, id)
	}
}

// Only registered, unbound, expired files are collected. Never scan/delete
// legacy files of unknown ownership or attachments already bound to a ticket.
func (s *Service) StartUploadMaintenance(ctx context.Context, root string) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		batchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		items, err := s.repo.ExpiredUploads(batchCtx)
		if err != nil {
			log.Print("ticket_upload_gc database_unavailable")
			cancel()
			continue
		}
		for _, item := range items {
			if batchCtx.Err() != nil {
				break
			}
			if _, err := uuid.Parse(item.ID); err != nil {
				continue
			}
			if err := os.Remove(filepath.Join(root, item.ID)); err == nil || os.IsNotExist(err) {
				_ = s.repo.FinishDelete(batchCtx, item.ID)
			} else {
				log.Print("ticket_upload_gc file_remove_failed")
			}
		}
		cancel()
	}
}
