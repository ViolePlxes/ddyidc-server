package adminread

import "testing"

func TestBankTransferValidation(t *testing.T) {
	cases := []struct {
		name  string
		c     bankTransferConfig
		valid bool
	}{
		{"disabled empty", bankTransferConfig{}, true},
		{"enabled incomplete", bankTransferConfig{Enabled: true}, false},
		{"account invalid", bankTransferConfig{BankAccount: "1234abc"}, false},
		{"bank code invalid", bankTransferConfig{BankCode: "123"}, false},
		{"multiline name", bankTransferConfig{CompanyName: "company\nother"}, false},
		{"valid", bankTransferConfig{Enabled: true, CompanyName: "Company", BankName: "Bank", BankAccount: "123456789012", BankCode: "123456789012"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := normalizeBankTransfer(&tc.c); (err == nil) != tc.valid {
				t.Fatalf("unexpected validation: %v", err)
			}
		})
	}
}
func TestDisabledBankTransferHidesReceivingDetails(t *testing.T) {
	c := bankTransferConfig{CompanyName: "private", BankAccount: "123456789012", Instructions: "private"}
	if got := publicBankTransfer(c); got != (bankTransferConfig{}) {
		t.Fatal("disabled receiving details leaked")
	}
	c.Enabled = true
	if got := publicBankTransfer(c); got != c {
		t.Fatal("enabled receiving details missing")
	}
}
