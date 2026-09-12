package adminread

import (
	"net/http/httptest"
	"testing"
)

func TestPageParams(t *testing.T) {
	req := httptest.NewRequest("GET", "/?page=3&pageSize=50", nil)
	page, pageSize := pageParams(req)
	if page != 3 || pageSize != 50 {
		t.Fatalf("page=%d pageSize=%d", page, pageSize)
	}
	req = httptest.NewRequest("GET", "/?page=-1&pageSize=101", nil)
	page, pageSize = pageParams(req)
	if page != 1 || pageSize != 20 {
		t.Fatalf("fallback page=%d pageSize=%d", page, pageSize)
	}
}

func TestOptionalStatus(t *testing.T) {
	req := httptest.NewRequest("GET", "/?status=2", nil)
	status, ok := optionalStatus(req, 1, 6)
	if !ok || status == nil || *status != 2 {
		t.Fatalf("valid status rejected")
	}
	req = httptest.NewRequest("GET", "/?status=9", nil)
	if _, ok := optionalStatus(req, 1, 6); ok {
		t.Fatalf("invalid status accepted")
	}
}

func TestMaskPII(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"phone", maskPhone("13812348021"), "138****8021"},
		{"short phone", maskPhone("123"), "***"},
		{"empty phone", maskPhone(""), ""},
		{"email", maskEmail("user@example.com"), "u***@example.com"},
		{"invalid email", maskEmail("value"), "***"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("got %q want %q", test.got, test.want)
			}
		})
	}
}

func TestNormalizeCustomerInput(t *testing.T) {
	tests := []struct {
		name      string
		input     createCustomerInput
		wantPhone string
		wantEmail string
		wantError string
	}{
		{
			name:      "mainland mobile",
			input:     createCustomerInput{Name: "测试用户", CountryCode: "+86", Mobile: "13800138000", Password: "fixture-secret-9"},
			wantPhone: "13800138000",
		},
		{
			name:      "international mobile",
			input:     createCustomerInput{Name: "Example Inc", CountryCode: "+1", Mobile: "2025550198", Password: "fixture-secret-9"},
			wantPhone: "+12025550198",
		},
		{
			name:      "email normalized",
			input:     createCustomerInput{Name: "邮箱用户", Email: "USER@Example.com", Password: "fixture-secret-9"},
			wantEmail: "user@example.com",
		},
		{
			name:      "contact required",
			input:     createCustomerInput{Name: "无联系方式", Password: "fixture-secret-9"},
			wantError: "手机号与邮箱至少填写一项",
		},
		{
			name:      "mainland mobile format",
			input:     createCustomerInput{Name: "错误手机号", CountryCode: "+86", Mobile: "123", Password: "fixture-secret-9"},
			wantError: "请输入正确的 11 位中国大陆手机号",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, message := normalizeCustomerInput(test.input)
			if message != test.wantError {
				t.Fatalf("message=%q want=%q", message, test.wantError)
			}
			if got.Phone != test.wantPhone || got.Email != test.wantEmail {
				t.Fatalf("phone=%q email=%q", got.Phone, got.Email)
			}
		})
	}
}
