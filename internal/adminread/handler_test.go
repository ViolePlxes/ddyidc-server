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
