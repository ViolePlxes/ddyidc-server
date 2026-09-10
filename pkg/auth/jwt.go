package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims JWT 载荷
type Claims struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// Manager JWT 管理器
type Manager struct {
	accessSecret  []byte
	accessExpire  time.Duration
	refreshSecret []byte
	refreshExpire time.Duration
	security      SecurityConfig
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
	claims := Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(m.accessExpire)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
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
