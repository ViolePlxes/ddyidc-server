package withdrawal

import (
	billing "ddyidc/internal/billing/balance"
	"testing"
)

func TestEligibleConservativeCash(t *testing.T) {
	rows := []billing.Transaction{
		{AccountVersion: 1, Type: "commission", Amount: 10000, BalanceAfter: 10000},
		{AccountVersion: 2, Type: "points_credit", Amount: 2000, BalanceBefore: 10000, BalanceAfter: 12000},
		{AccountVersion: 3, Type: "order_pay", Amount: -3000, BalanceBefore: 12000, BalanceAfter: 9000},
		{AccountVersion: 4, Type: "withdrawal_hold", RelatedID: 42, Amount: -2000, BalanceBefore: 9000, BalanceAfter: 7000, FrozenAfter: 2000},
		{AccountVersion: 5, Type: "withdrawal_release", RelatedID: 42, Amount: 2000, BalanceBefore: 7000, BalanceAfter: 9000, FrozenBefore: 2000},
	}
	got, err := Eligible(rows, map[string]bool{"commission": true})
	if err != nil || got != 7000 {
		t.Fatalf("got %v %v", got, err)
	}
	rows[2].AccountVersion = 9
	if _, err = Eligible(rows, map[string]bool{"commission": true}); err == nil {
		t.Fatal("broken history accepted")
	}
}
func TestUnknownCreditNotWithdrawable(t *testing.T) {
	rows := []billing.Transaction{{AccountVersion: 1, Type: "manual_credit", Amount: 10000, BalanceAfter: 10000}}
	got, err := Eligible(rows, map[string]bool{"commission": true})
	if err != nil || got != 0 {
		t.Fatal(got, err)
	}
}
