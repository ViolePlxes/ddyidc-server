package adminread

import "testing"

func TestOrderCustomerDisplayName(t *testing.T) {
	tests := []struct {
		name string
		row  orderRow
		want string
	}{
		{"verified name", orderRow{CustomerID: "9007199254740993", VerifiedName: " 张三 ", RealnameStatus: 2, CustomerPhone: "13800138000"}, "张三"},
		{"pending name is not verified", orderRow{CustomerID: "12", VerifiedName: "待审核姓名", RealnameStatus: 1, CustomerPhone: "13800138000"}, "13800138000"},
		{"rejected name is not verified", orderRow{CustomerID: "12", VerifiedName: "驳回姓名", RealnameStatus: 3, CustomerPhone: "13800138000"}, "13800138000"},
		{"no realname", orderRow{CustomerID: "12", CustomerPhone: " +12025550198 "}, "+12025550198"},
		{"blank verified name falls back", orderRow{CustomerID: "12", VerifiedName: " ", RealnameStatus: 2, CustomerPhone: "13800138000"}, "13800138000"},
		{"no phone preserves full id", orderRow{CustomerID: "9007199254740993", Username: "must-not-be-used", CustomerPhone: " "}, "9007199254740993"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := orderCustomerDisplayName(tt.row); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
