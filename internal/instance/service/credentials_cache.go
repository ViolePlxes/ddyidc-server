package instance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/upstream/provider/contracts"
	crypt "ddyidc/pkg/crypto"
)

var errNoCachedCredentials = errors.New("没有缓存的登录信息")

// Supplier login details are cached encrypted in config_snapshot when a sync or
// a successful credential read happens. The supplier stays authoritative, but a
// transient read failure no longer hides the login info from the operator.
const credentialsSnapshotKey = "credentialsCiphertext"

type cachedCredentialPayload struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	Port          string `json:"port"`
	PortIsDefault bool   `json:"portIsDefault,omitempty"`
}

func (s *Service) cacheSupplierCredentials(fields map[string]any, detail *contracts.InstanceDetail) {
	if len(s.secretKey) != 32 || detail == nil {
		return
	}
	if strings.TrimSpace(detail.Username) == "" && strings.TrimSpace(detail.Password) == "" {
		return
	}
	raw, ok := fields["config_snapshot"].(string)
	if !ok || raw == "" {
		return
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &snapshot) != nil || snapshot == nil {
		return
	}
	payload, err := json.Marshal(cachedCredentialPayload{Username: detail.Username, Password: detail.Password, Port: detail.Port, PortIsDefault: detail.PortIsDefault})
	if err != nil {
		return
	}
	encrypted, err := crypt.Encrypt(string(payload), s.secretKey)
	if err != nil {
		return
	}
	encoded, err := json.Marshal(encrypted)
	if err != nil {
		return
	}
	snapshot[credentialsSnapshotKey] = encoded
	out, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	fields["config_snapshot"] = string(out)
}

func (s *Service) cachedCredentials(h *dom.Host) (*Credentials, error) {
	if len(s.secretKey) != 32 || h == nil {
		return nil, errNoCachedCredentials
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal([]byte(h.ConfigSnapshot), &snapshot) != nil {
		return nil, errNoCachedCredentials
	}
	raw, ok := snapshot[credentialsSnapshotKey]
	if !ok {
		return nil, errNoCachedCredentials
	}
	var encrypted string
	if json.Unmarshal(raw, &encrypted) != nil || encrypted == "" {
		return nil, errNoCachedCredentials
	}
	plain, err := crypt.Decrypt(encrypted, s.secretKey)
	if err != nil {
		return nil, errNoCachedCredentials
	}
	var payload cachedCredentialPayload
	if json.Unmarshal([]byte(plain), &payload) != nil {
		return nil, errNoCachedCredentials
	}
	return &Credentials{Username: payload.Username, Password: payload.Password, Port: payload.Port, PortIsDefault: payload.PortIsDefault}, nil
}

// persistCachedCredentials keeps the encrypted copy in step with the supplier
// after a successful live read. Failures are silent: the live result is enough.
func (s *Service) persistCachedCredentials(h *dom.Host, detail *contracts.InstanceDetail) {
	if len(s.secretKey) != 32 || s.db == nil || h == nil || detail == nil {
		return
	}
	fields := map[string]any{"config_snapshot": h.ConfigSnapshot}
	s.cacheSupplierCredentials(fields, detail)
	updated, ok := fields["config_snapshot"].(string)
	if !ok || updated == h.ConfigSnapshot {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.db.WithContext(ctx).Model(&dom.Host{}).
		Where("id=? AND ownership_version=? AND upstream_instance_id=? AND config_snapshot=?", h.ID, h.OwnershipVersion, h.UpstreamInstanceID, h.ConfigSnapshot).
		Update("config_snapshot", updated).Error
}
