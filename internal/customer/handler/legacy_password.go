package customer

import (
	"context"
	"errors"
	"net/http"

	domain "ddyidc/internal/customer/domain"
	"ddyidc/pkg/auth"
)

type v10PasswordUpgrader interface {
	UpgradeV10Password(context.Context, *domain.Customer, string, string, string) (*domain.Customer, error)
}

func (h *authHandler) verifyCustomerPassword(r *http.Request, customer *domain.Customer, password string) (*domain.Customer, bool, error) {
	if customer == nil || customer.Status != 1 {
		return customer, false, nil
	}
	if !auth.IsV10Password(customer.PasswordHash) {
		return customer, auth.PasswordMatches(customer.PasswordHash, password), nil
	}
	key, err := auth.LoadV10AuthCode()
	if err != nil {
		return nil, false, err
	}
	if !auth.VerifyV10Password(customer.PasswordHash, password, key) {
		return customer, false, nil
	}
	upgrader, ok := h.customers.(v10PasswordUpgrader)
	if !ok {
		return nil, false, errors.New("旧密码升级服务未就绪")
	}
	hash, err := auth.HashMigratedPassword(password)
	if err != nil {
		return nil, false, err
	}
	updated, err := upgrader.UpgradeV10Password(r.Context(), customer, hash, password, clientIP(r))
	return updated, err == nil, err
}
