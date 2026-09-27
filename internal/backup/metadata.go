package backup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime/debug"
	"time"
)

type SchemaVersion struct {
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
	State    string `json:"state"`
}
type SnapshotMetadata struct {
	StartedAt         time.Time       `json:"startedAt"`
	FinishedAt        time.Time       `json:"finishedAt"`
	Application       string          `json:"application"`
	ApplicationSHA256 string          `json:"applicationSHA256"`
	Worker            string          `json:"worker"`
	Schemas           []SchemaVersion `json:"schemas"`
	IDHighWater       int64           `json:"idHighWater"`
}

func manifestMAC(m Manifest, key []byte) string {
	m.MAC = ""
	raw, _ := json.Marshal(m)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("ddyidc-business-backup-manifest-v2\x00"))
	mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) snapshotMetadata(ctx context.Context) (*SnapshotMetadata, error) {
	// The worker's build info is not the application version. Bind the snapshot
	// to the installed application artifact as well as its schema checksums.
	applicationHash, err := fileSHA("/opt/ddyidc/bin/ddyidc-server")
	if err != nil {
		return nil, errors.New("无法核验已安装应用版本，禁止备份或自动恢复")
	}
	m := &SnapshotMetadata{Application: "ddyidc-server", ApplicationSHA256: applicationHash, Worker: "unversioned"}
	if info, ok := debug.ReadBuildInfo(); ok {
		m.Worker = info.Main.Path + "@" + info.Main.Version
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				m.Worker += "/" + setting.Value
			}
		}
	}
	if err := s.db.WithContext(ctx).Raw("SELECT CURRENT_TIMESTAMP(6)").Row().Scan(&m.StartedAt); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Table("system.schema_migrations").Order("name ASC").Find(&m.Schemas).Error; err != nil {
		return nil, err
	}
	if len(m.Schemas) == 0 {
		return nil, errors.New("缺少数据库版本证据，禁止备份")
	}
	for _, v := range m.Schemas {
		if v.State != "complete" {
			return nil, errors.New("数据库迁移未完成，禁止备份")
		}
	}
	if err := s.db.WithContext(ctx).Raw("SELECT high_water FROM system.id_sequences WHERE name='global'").Row().Scan(&m.IDHighWater); err != nil {
		return nil, err
	}
	return m, nil
}

func metadataCompatible(saved, current *SnapshotMetadata) error {
	if saved == nil || current == nil || saved.FinishedAt.IsZero() || saved.FinishedAt.Before(saved.StartedAt) || saved.IDHighWater <= 0 || len(saved.Schemas) != len(current.Schemas) {
		return errors.New("备份缺少可核验版本信息，需要离线迁移恢复")
	}
	if len(saved.ApplicationSHA256) != 64 || saved.ApplicationSHA256 != current.ApplicationSHA256 {
		return errors.New("备份应用版本与当前产物不一致，需要隔离环境核验兼容性后离线恢复")
	}
	for n, v := range saved.Schemas {
		if v != current.Schemas[n] || v.State != "complete" {
			return errors.New("备份与当前数据库版本不兼容，需要离线迁移恢复")
		}
	}
	return nil
}
