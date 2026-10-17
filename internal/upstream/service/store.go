package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/upstream/model"
	"ddyidc/internal/upstream/provider"
	cryptoutil "ddyidc/pkg/crypto"
	"ddyidc/pkg/db"
	"gorm.io/gorm"
)

// Store persists upstream resources. The maps only exist when NewMemoryStore is
// explicitly chosen by a test; production must use NewStore after db.Init.
type Store struct {
	quoteFlightMu sync.Mutex
	quoteFlights  map[string]*quoteFlight
	quoteSlots    chan struct{}
	db            *gorm.DB
	aesKey        []byte
	mu            sync.RWMutex
	suppliers     map[string]model.Supplier
	products      map[string]model.UpstreamProduct
	orders        map[string]model.UpstreamOrder
	hosts         map[string]model.UpstreamHost
	taskCancels   map[string]context.CancelFunc
	taskSuppliers map[string]string
	connectors    map[string]bool
	moduleStates  map[string]ModuleState
	taskSem       chan struct{}
}

func NewStore(database *gorm.DB, aesKey []byte) *Store {
	return &Store{quoteSlots: make(chan struct{}, 4), db: database, aesKey: append([]byte(nil), aesKey...), taskCancels: map[string]context.CancelFunc{}, taskSuppliers: map[string]string{}, connectors: map[string]bool{}, moduleStates: map[string]ModuleState{}, taskSem: make(chan struct{}, 2)}
}

func NewDefaultStore(aesKey []byte) *Store { return NewStore(db.G, aesKey) }

func NewMemoryStore(aesKey []byte) *Store {
	return &Store{quoteSlots: make(chan struct{}, 4), aesKey: append([]byte(nil), aesKey...), suppliers: map[string]model.Supplier{}, products: map[string]model.UpstreamProduct{}, orders: map[string]model.UpstreamOrder{}, hosts: map[string]model.UpstreamHost{}, taskCancels: map[string]context.CancelFunc{}, taskSuppliers: map[string]string{}, connectors: map[string]bool{}, moduleStates: map[string]ModuleState{}, taskSem: make(chan struct{}, 2)}
}

func (s *Store) isMemory() bool { return s.db == nil }
func id() string                { return strconv.FormatInt(snowflake.GenID(), 10) }
func now() int64                { return time.Now().Unix() }
func (s *Store) encrypt(value string) (string, error) {
	if len(s.aesKey) == 0 {
		return "", errors.New("upstream AES key is not configured")
	}
	return cryptoutil.Encrypt(value, s.aesKey)
}
func (s *Store) decrypt(value string) (string, error) {
	if len(s.aesKey) == 0 {
		return "", errors.New("upstream AES key is not configured")
	}
	return cryptoutil.Decrypt(value, s.aesKey)
}
func toProvider(v model.Supplier) provider.Supplier {
	return provider.Supplier{ID: v.ID, Name: v.Name, Type: v.Type, URL: v.URL, Username: v.Username, Token: v.Token, Secret: v.Secret, Rate: v.Rate, CurrencyCode: v.CurrencyCode, Status: v.Status}
}

// Default is initialized in main only after the database and crypto config are ready.
var Default *Store
