package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Claims JWT 载荷
type Claims struct {
	DeviceID       string `json:"device_id,omitempty"`
	AuthVersion    uint64 `json:"auth_version,omitempty"`
	ActorID        int64  `json:"actor_id,omitempty"`
	ActorVersion   uint64 `json:"actor_version,omitempty"`
	ActorSessionID string `json:"actor_session_id,omitempty"`
	UserID         int64  `json:"user_id"`
	Username       string `json:"username"`
	Role           string `json:"role"`
	jwt.RegisteredClaims
}

// Manager JWT 管理器
type Manager struct {
	devicesEnabled bool
	accounts       *gorm.DB
	accountState   func(context.Context, SessionKind, int64) (uint64, error)
	accessSecret   []byte
	accessExpire   time.Duration
	refreshSecret  []byte
	refreshExpire  time.Duration
	security       SecurityConfig
	clock          func() time.Time
}

// NewManager 创建JWT管理器
func NewManager(accessSecret, refreshSecret string, accessExpire, refreshExpire int64) *Manager {
	return &Manager{
		accessSecret:  []byte(accessSecret),
		accessExpire:  time.Duration(accessExpire) * time.Second,
		refreshSecret: []byte(refreshSecret),
		refreshExpire: time.Duration(refreshExpire) * time.Second,
		security:      DefaultSecurityConfig(),
	}
}

// GenerateAccessToken 生成访问令牌
func (m *Manager) GenerateAccessToken(userID int64, username, role string) (string, error) {
	return m.generateVersionedToken(userID, username, role, 0, nil)
}

func (m *Manager) generateVersionedToken(userID int64, username, role string, version uint64, actor *Claims) (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", errors.New("cannot generate session identity")
	}
	now := time.Now()
	if m.clock != nil {
		now = m.clock()
	}
	claims := Claims{
		AuthVersion: version,
		UserID:      userID,
		Username:    username,
		Role:        role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        id.String(),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessExpire)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	if actor != nil {
		claims.ActorID, claims.ActorVersion, claims.ActorSessionID = actor.UserID, actor.AuthVersion, actor.ID
		if actor.ExpiresAt != nil && actor.ExpiresAt.Before(claims.ExpiresAt.Time) {
			claims.ExpiresAt = actor.ExpiresAt
		}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(m.accessSecret)
}

// ParseAccessToken 解析访问令牌
func (m *Manager) ParseAccessToken(tokenStr string) (*Claims, error) {
	claims, err := m.parseAccessToken(tokenStr)
	if err != nil {
		return nil, err
	}
	kind, err := sessionKindForRole(claims.Role)
	if err != nil {
		return nil, err
	}
	if err := m.validateSession(context.Background(), kind, claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func (m *Manager) ParseAccessTokenFor(ctx context.Context, kind SessionKind, tokenStr string) (*Claims, error) {
	claims, err := m.parseAccessToken(tokenStr)
	if err != nil {
		return nil, err
	}
	if err := m.validateSession(ctx, kind, claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func sessionKindForRole(role string) (SessionKind, error) {
	switch role {
	case "customer":
		return SessionUser, nil
	case "admin":
		return SessionAdmin, nil
	case "sales":
		return SessionSales, nil
	default:
		return "", errors.New("unsupported session role")
	}
}

func (m *Manager) parseAccessToken(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method: %s", t.Method.Alg())
		}
		return m.accessSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}
