package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// VerifyOSS deliberately ignores the local copy. It never imports SQL, fences
// runtime accounts, starts services, or releases a maintenance barrier.
func (s *Service) VerifyOSS(ctx context.Context, id string) error {
	if s.privateRoot == "" || len(s.signingKey) != 32 {
		return errors.New("独立维护目录和签名密钥尚未配置")
	}
	source, err := s.Job(ctx, id)
	if err != nil {
		return errors.New("原备份不存在")
	}
	source.LocalPath = ""
	archive, err := s.Archive(ctx, source)
	if err != nil {
		return err
	}
	defer os.Remove(archive)
	payload, err := os.MkdirTemp(filepath.Join(s.privateRoot, "jobs"), "verify-")
	if err != nil {
		return errors.New("无法创建独立校验目录")
	}
	defer os.RemoveAll(payload)
	var metadata *SnapshotMetadata
	if err = unpackSigned(archive, payload, s.key, s.signingKey, &metadata); err != nil {
		return err
	}
	current, err := s.snapshotMetadata(ctx)
	if err != nil {
		return err
	}
	return metadataCompatible(metadata, current)
}
