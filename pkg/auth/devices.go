package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"ddyidc/pkg/cache"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const AccessLifetime = 2 * time.Hour
const RefreshLifetime = 30 * 24 * time.Hour

// Only a digest of a cryptographically random credential is persisted.
// Password hashing is deliberately separate: these tokens have 256 bits of entropy.
type Device struct {
	ID           string    `json:"id"`
	UserID       int64     `json:"-"`
	Account      string    `json:"-"`
	AuthVersion  uint64    `json:"-"`
	RefreshHash  string    `json:"-"`
	Browser      string    `json:"browser"`
	IP           string    `json:"ip"`
	LoginAt      time.Time `json:"loginAt"`
	LastActiveAt time.Time `json:"lastActiveAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Current      bool      `json:"current" gorm:"-"`
}

func (Device) TableName() string { return "customer.auth_devices" }

type TokenPair struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}

var ErrRefreshRejected = errors.New("refresh credential expired or revoked")

func deviceRedisKey(d *Device) string {
	// Included by the existing disaster-recovery session:user:* purge.
	return fmt.Sprintf("session:user:%d:device:%s", d.UserID, d.ID)
}
func validateDeviceMarker(ctx context.Context, d *Device) error {
	if cache.RDB == nil {
		return errors.New("session service unavailable")
	}
	value, err := cache.RDB.Get(ctx, deviceRedisKey(d)).Result()
	if errors.Is(err, redis.Nil) {
		return ErrRefreshRejected
	}
	if err != nil {
		return err
	}
	if value != fmt.Sprint(d.AuthVersion) {
		return ErrRefreshRejected
	}
	return nil
}

func refreshDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func newRefresh() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}
func (m *Manager) deviceToken(d *Device) (string, error) {
	now := time.Now()
	claims := Claims{DeviceID: d.ID, UserID: d.UserID, Username: d.Account, Role: "customer", AuthVersion: d.AuthVersion,
		RegisteredClaims: jwt.RegisteredClaims{ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(AccessLifetime))}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.accessSecret)
}

// The account row serializes login/refresh/revocation with password and status
// changes. The verified credential version remains the login barrier.
func lockDeviceAccount(tx *gorm.DB, userID int64, version uint64) error {
	var row struct {
		AuthVersion uint64
		Status      int
	}
	if err := tx.Table("customer.customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("auth_version,status").Where("id=? AND deleted_at IS NULL", userID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRefreshRejected
		}
		return err
	}
	if row.Status != 1 || row.AuthVersion != version {
		return ErrRefreshRejected
	}
	return nil
}
func (m *Manager) IssueDeviceSession(ctx context.Context, userID int64, account string, version uint64, browser, ip string) (TokenPair, error) {
	var pair TokenPair
	if !m.devicesEnabled || cache.RDB == nil || cache.RDB.Ping(ctx).Err() != nil {
		return pair, errors.New("session service unavailable")
	}
	refresh, err := newRefresh()
	if err != nil {
		return pair, err
	}
	now := time.Now()
	d := Device{ID: uuid.NewString(), UserID: userID, Account: account, AuthVersion: version, RefreshHash: refreshDigest(refresh), Browser: browser, IP: ip, LoginAt: now, LastActiveAt: now, ExpiresAt: now.Add(RefreshLifetime)}
	token, err := m.deviceToken(&d)
	if err != nil {
		return pair, err
	}
	err = m.accounts.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockDeviceAccount(tx, userID, version); err != nil {
			return err
		}
		// Drop expired/old-version credentials before enforcing the device ceiling.
		if err := tx.Where("user_id=? AND (expires_at<=? OR auth_version<>?)", userID, now, version).Delete(&Device{}).Error; err != nil {
			return err
		}
		var devices []Device
		if err := tx.Where("user_id=?", userID).Order("login_at ASC, id ASC").Find(&devices).Error; err != nil {
			return err
		}
		if len(devices) >= 10 {
			ids := make([]string, 0, len(devices)-9)
			for _, old := range devices[:len(devices)-9] {
				ids = append(ids, old.ID)
			}
			if err := tx.Where("id IN ?", ids).Delete(&Device{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&d).Error; err != nil {
			return err
		}
		return cache.RDB.Set(ctx, deviceRedisKey(&d), fmt.Sprint(version), RefreshLifetime).Err()
	})
	if err != nil {
		_ = cache.RDB.Del(ctx, deviceRedisKey(&d)).Err()
		return pair, err
	}
	return TokenPair{token, refresh, int64(AccessLifetime.Seconds()), int64(RefreshLifetime.Seconds())}, nil
}

func (m *Manager) RefreshDeviceSession(ctx context.Context, refresh string) (TokenPair, error) {
	var pair TokenPair
	if !m.devicesEnabled || cache.RDB == nil || cache.RDB.Ping(ctx).Err() != nil {
		return pair, errors.New("session service unavailable")
	}
	if len(refresh) != 43 {
		return pair, ErrRefreshRejected
	}
	digest := refreshDigest(refresh)
	var lookup Device
	if err := m.accounts.WithContext(ctx).Where("refresh_hash=?", digest).Take(&lookup).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return pair, ErrRefreshRejected
		}
		return pair, err
	}
	next, err := newRefresh()
	if err != nil {
		return pair, err
	}
	err = m.accounts.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockDeviceAccount(tx, lookup.UserID, lookup.AuthVersion); err != nil {
			return err
		}
		var d Device
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND refresh_hash=? AND expires_at>?", lookup.ID, digest, time.Now()).Take(&d).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRefreshRejected
			}
			return err
		}
		if err := validateDeviceMarker(ctx, &d); err != nil {
			return err
		}
		token, err := m.deviceToken(&d)
		if err != nil {
			return err
		}
		now := time.Now()
		if err := tx.Model(&d).Updates(map[string]any{"refresh_hash": refreshDigest(next), "last_active_at": now, "expires_at": now.Add(RefreshLifetime)}).Error; err != nil {
			return err
		}
		pair = TokenPair{token, next, int64(AccessLifetime.Seconds()), int64(RefreshLifetime.Seconds())}
		// Never recreate a marker cleared by recovery or revocation.
		extended, err := cache.RDB.Expire(ctx, deviceRedisKey(&d), RefreshLifetime).Result()
		if err != nil {
			return err
		}
		if !extended {
			return ErrRefreshRejected
		}
		return nil
	})
	return pair, err
}

func (m *Manager) validateDeviceSession(ctx context.Context, kind SessionKind, claims *Claims) error {
	if kind != SessionUser || claims.Role != "customer" || claims.ActorID != 0 || claims.ID == "" || claims.IssuedAt == nil || !m.devicesEnabled || cache.RDB == nil {
		return errors.New("invalid device session")
	}
	// Retain fail-closed Redis availability and authoritative account-version checks.
	if err := validateDeviceMarker(ctx, &Device{ID: claims.DeviceID, UserID: claims.UserID, AuthVersion: claims.AuthVersion}); err != nil {
		return err
	}
	version, err := m.accountVersion(ctx, kind, claims.UserID)
	if err != nil || version != claims.AuthVersion {
		return ErrRefreshRejected
	}
	var d Device
	if err := m.accounts.WithContext(ctx).Where("id=? AND user_id=? AND account=? AND auth_version=? AND expires_at>?", claims.DeviceID, claims.UserID, claims.Username, claims.AuthVersion, time.Now()).Take(&d).Error; err != nil {
		return ErrRefreshRejected
	}
	// Activity is throttled to one write per minute and cannot revive revoked rows.
	now := time.Now()
	if now.Sub(d.LastActiveAt) >= time.Minute {
		if err := m.accounts.WithContext(ctx).Model(&Device{}).Where("id=? AND last_active_at<?", d.ID, now.Add(-time.Minute)).Update("last_active_at", now).Error; err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) ListDevices(ctx context.Context, claims *Claims) ([]Device, error) {
	if err := m.validateSession(ctx, SessionUser, claims); err != nil {
		return nil, err
	}
	var devices []Device
	err := m.accounts.WithContext(ctx).Where("user_id=? AND auth_version=? AND expires_at>?", claims.UserID, claims.AuthVersion, time.Now()).Order("login_at DESC, id DESC").Find(&devices).Error
	if err != nil {
		return nil, err
	}
	active := make([]Device, 0, len(devices))
	for _, device := range devices {
		if markerErr := validateDeviceMarker(ctx, &device); markerErr != nil {
			if errors.Is(markerErr, ErrRefreshRejected) {
				continue
			}
			return nil, markerErr
		}
		device.Current = device.ID == claims.DeviceID
		active = append(active, device)
	}
	return active, nil
}
func (m *Manager) RevokeDevice(ctx context.Context, claims *Claims, id string) error {
	if err := m.validateSession(ctx, SessionUser, claims); err != nil {
		return err
	}
	return m.accounts.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockDeviceAccount(tx, claims.UserID, claims.AuthVersion); err != nil {
			return err
		}
		result := tx.Where("id=? AND user_id=?", id, claims.UserID).Delete(&Device{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}
func (m *Manager) revokeAllDevices(ctx context.Context, userID int64) error {
	return m.accounts.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var id int64
		if err := tx.Table("customer.customers").Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id=?", userID).Scan(&id).Error; err != nil {
			return err
		}
		return tx.Where("user_id=?", userID).Delete(&Device{}).Error
	})
}
func (m *Manager) SetDeviceCookie(w http.ResponseWriter, pair TokenPair, remember bool) {
	expires := time.Now().Add(AccessLifetime)
	maxAge := 0
	if remember {
		maxAge = int(AccessLifetime.Seconds())
	} else {
		expires = time.Time{}
	}
	http.SetCookie(w, &http.Cookie{Name: m.CookieName(SessionUser), Value: pair.AccessToken, Path: "/", MaxAge: maxAge, Expires: expires, HttpOnly: true, Secure: m.security.CookieSecure, SameSite: http.SameSiteStrictMode})
	if claims, err := m.parseAccessToken(pair.AccessToken); err == nil {
		http.SetCookie(w, &http.Cookie{Name: CSRFCookieName(SessionUser), Value: m.csrfValue(claims), Path: "/", MaxAge: maxAge, Expires: expires, Secure: m.security.CookieSecure, SameSite: http.SameSiteStrictMode})
	}
}
