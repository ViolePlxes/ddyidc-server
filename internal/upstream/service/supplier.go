package service

import (
	"fmt"
	"sort"
	"strings"

	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	"gorm.io/gorm"
)

type SupplierInput struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	URL          string  `json:"url"`
	Username     string  `json:"username"`
	Token        string  `json:"token"`
	Secret       string  `json:"secret"`
	Rate         float64 `json:"rate"`
	CurrencyCode string  `json:"currencyCode"`
	Status       int     `json:"status"`
	Remark       string  `json:"remark"`
}

func sanitizeSupplier(v model.Supplier) model.Supplier { v.Token = ""; v.Secret = ""; return v }

func (s *Store) CreateSupplier(in SupplierInput) (model.Supplier, error) {
	in.URL = strings.TrimRight(strings.TrimSpace(in.URL), "/")
	if in.Name == "" || in.URL == "" || in.Username == "" || in.Token == "" {
		return model.Supplier{}, fmt.Errorf("name, url, username and token are required")
	}
	if in.Type == "" {
		in.Type = "default"
	}
	if in.Rate == 0 {
		in.Rate = 1
	}
	if in.Status == 0 {
		in.Status = 1
	}
	token, e := s.encrypt(in.Token)
	if e != nil {
		return model.Supplier{}, e
	}
	secret, e := s.encrypt(in.Secret)
	if e != nil {
		return model.Supplier{}, e
	}
	v := model.Supplier{Name: in.Name, Type: in.Type, URL: in.URL, Username: in.Username, Token: token, Secret: secret, Rate: in.Rate, CurrencyCode: in.CurrencyCode, Status: in.Status, Remark: in.Remark}
	if s.isMemory() {
		s.mu.Lock()
		v.ID = id()
		v.CreatedAt = now()
		v.UpdatedAt = v.CreatedAt
		s.suppliers[v.ID] = v
		s.mu.Unlock()
	} else if e := s.db.Table(v.TableName()).Create(&v).Error; e != nil {
		return model.Supplier{}, e
	}
	return sanitizeSupplier(v), nil
}
func (s *Store) ListSuppliers() (out []model.Supplier) {
	if s.isMemory() {
		s.mu.RLock()
		for _, v := range s.suppliers {
			out = append(out, sanitizeSupplier(v))
		}
		s.mu.RUnlock()
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
		return
	}
	if s.db.Table((model.Supplier{}).TableName()).Order("created_at DESC").Find(&out).Error != nil {
		return nil
	}
	for i := range out {
		out[i] = sanitizeSupplier(out[i])
	}
	return
}
func (s *Store) GetSupplier(key string) (model.Supplier, error) {
	if s.isMemory() {
		s.mu.RLock()
		v, ok := s.suppliers[key]
		s.mu.RUnlock()
		if !ok {
			return model.Supplier{}, fmt.Errorf("supplier not found")
		}
		return v, nil
	}
	var v model.Supplier
	e := s.db.Table((model.Supplier{}).TableName()).Where("id = ?", key).First(&v).Error
	if e == gorm.ErrRecordNotFound {
		return model.Supplier{}, fmt.Errorf("supplier not found")
	}
	return v, e
}
func (s *Store) UpdateSupplier(key string, in SupplierInput) (model.Supplier, error) {
	in.URL = strings.TrimRight(strings.TrimSpace(in.URL), "/")
	v, e := s.GetSupplier(key)
	if e != nil {
		return v, e
	}
	if in.Name != "" {
		v.Name = in.Name
	}
	if in.URL != "" {
		v.URL = in.URL
	}
	if in.Username != "" {
		v.Username = in.Username
	}
	if in.Type != "" {
		v.Type = in.Type
	}
	if in.Rate > 0 {
		v.Rate = in.Rate
	}
	if in.CurrencyCode != "" {
		v.CurrencyCode = in.CurrencyCode
	}
	if in.Status != 0 {
		v.Status = in.Status
	}
	v.Remark = in.Remark
	if in.Token != "" {
		v.Token, e = s.encrypt(in.Token)
		if e != nil {
			return v, e
		}
	}
	if in.Secret != "" {
		v.Secret, e = s.encrypt(in.Secret)
		if e != nil {
			return v, e
		}
	}
	if s.isMemory() {
		s.mu.Lock()
		v.UpdatedAt = now()
		s.suppliers[key] = v
		s.mu.Unlock()
	} else if e = s.db.Table(v.TableName()).Where("id = ?", key).Save(&v).Error; e != nil {
		return model.Supplier{}, e
	}
	return sanitizeSupplier(v), nil
}
func (s *Store) DeleteSupplier(key string) error {
	if s.isMemory() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.suppliers[key]; !ok {
			return fmt.Errorf("supplier not found")
		}
		delete(s.suppliers, key)
		return nil
	}
	r := s.db.Table((model.Supplier{}).TableName()).Where("id = ?", key).Delete(&model.Supplier{})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected == 0 {
		return fmt.Errorf("supplier not found")
	}
	return nil
}
func (s *Store) ProviderFor(key string) (provider.UpstreamProvider, error) {
	return s.providerFor(key, false)
}

func (s *Store) catalogueProvider(key string) (provider.UpstreamProvider, error) {
	return s.providerFor(key, true)
}

func (s *Store) providerFor(key string, limited bool) (provider.UpstreamProvider, error) {
	v, e := s.GetSupplier(key)
	if e != nil {
		return nil, e
	}
	v.Token, e = s.decrypt(v.Token)
	if e != nil {
		return nil, e
	}
	v.Secret, e = s.decrypt(v.Secret)
	if e != nil {
		return nil, e
	}
	config := toProvider(v)
	config.LimitRequests = limited
	return provider.NewProvider(config)
}
func (s *Store) TestSupplier(key string) error {
	p, e := s.ProviderFor(key)
	if e != nil {
		return e
	}
	return p.Auth()
}
