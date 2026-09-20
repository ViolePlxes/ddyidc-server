package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"errors"
	"strings"
	"testing"
)

func TestRemarkValidation(t *testing.T) {
	if _, e := remarkValue(RemarkInput{}); e == nil {
		t.Fatal("missing accepted")
	}
	for _, v := range []string{strings.Repeat("备", 1001), "invalid\x00"} {
		if _, e := remarkValue(RemarkInput{&v}); e == nil {
			t.Fatal("invalid accepted")
		}
	}
	for _, v := range []string{"", strings.Repeat("备", 1000), " 生产实例 "} {
		got, e := remarkValue(RemarkInput{&v})
		if e != nil || got != strings.TrimSpace(v) {
			t.Fatal("valid rejected")
		}
	}
}
func TestRemarkRejectsAnotherCustomer(t *testing.T) {
	svc := NewService(&ownershipRepo{host: &dom.Host{ID: 100, CustomerID: 200}}, nil)
	v := "note"
	if e := svc.SaveRemark(context.Background(), 201, 100, RemarkInput{&v}); !errors.Is(e, ErrForbidden) {
		t.Fatalf("ownership: %v", e)
	}
}
func TestListCarriesLocalRemark(t *testing.T) {
	if listItem(&dom.Host{Remark: "生产实例"}).Remark != "生产实例" {
		t.Fatal("remark missing")
	}
}
