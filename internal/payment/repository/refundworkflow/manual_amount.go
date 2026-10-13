package refundworkflow

import balance "ddyidc/internal/billing/balance"

// SelectAmount applies an audited manual order decision without changing the daily estimate.
func SelectAmount(calc Calculation, mode, amount string) (Calculation, error) {
	switch mode {
	case "", "daily":
	case "full":
		calc.Refund = calc.Paid
		calc.Policy = "IDC_MANUAL_FULL_BALANCE_V1"
	case "custom":
		value, err := balance.ParseMoney(amount)
		if err != nil || value <= 0 || value > calc.Paid {
			return calc, ErrInput
		}
		calc.Refund = value
		calc.Policy = "IDC_MANUAL_CUSTOM_BALANCE_V1"
	default:
		return calc, ErrInput
	}
	return calc, nil
}
