package instance

import balance "ddyidc/internal/billing/balance"

func parseAdminMoney(value string) (int64, error) {
	v, e := balance.ParseMoney(value)
	return int64(v), e
}
