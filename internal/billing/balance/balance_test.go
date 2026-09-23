package billing

import "testing"

func TestChangeValidation(t *testing.T) {
	base := ChangeRequest{CustomerID: 1, Type: "manual_credit", Amount: "1.00", RefType: "customer", RefID: 1, Channel: "admin_manual", RequestID: "validation", AdminID: 1, AdminName: "admin", Notes: "reason", OperatorIP: "127.0.0.1"}
	if _, err := validate(base); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*ChangeRequest)
	}{
		{"missing type", func(r *ChangeRequest) { r.Type = "" }},
		{"missing amount", func(r *ChangeRequest) { r.Amount = "" }},
		{"missing ref", func(r *ChangeRequest) { r.RefType = "" }},
		{"missing ref id", func(r *ChangeRequest) { r.RefID = 0 }},
		{"missing channel", func(r *ChangeRequest) { r.Channel = "" }},
		{"missing request", func(r *ChangeRequest) { r.RequestID = "" }},
		{"missing actor", func(r *ChangeRequest) { r.AdminID = 0 }},
		{"missing IP", func(r *ChangeRequest) { r.OperatorIP = "" }},
		{"invalid IP", func(r *ChangeRequest) { r.OperatorIP = "forged" }},
		{"missing reason", func(r *ChangeRequest) { r.Notes = " " }},
		{"negative credit", func(r *ChangeRequest) { r.Amount = "-1.00" }},
		{"positive debit", func(r *ChangeRequest) { r.Type = "manual_debit" }},
		{"exponent", func(r *ChangeRequest) { r.Amount = "1e2" }},
		{"zero", func(r *ChangeRequest) { r.Amount = "0" }},
		{"precision", func(r *ChangeRequest) { r.Amount = "1.001" }},
		{"unknown type", func(r *ChangeRequest) { r.Type = "import" }},
		{"large unapproved", func(r *ChangeRequest) { r.Amount = "50000.01" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mutate(&r)
			if _, err := validate(r); err == nil {
				t.Fatal("invalid change accepted")
			}
		})
	}
}
