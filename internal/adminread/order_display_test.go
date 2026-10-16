package adminread

import (
	"ddyidc/internal/shared/testenv"
	"testing"
)

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

func TestOrderInstanceLinksMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	for _, sql := range []string{
		"CREATE TABLE `order`.orders(id BIGINT PRIMARY KEY,customer_id BIGINT)",
		"CREATE TABLE `order`.order_items(id BIGINT PRIMARY KEY,order_id BIGINT,instance_id BIGINT)",
		"CREATE TABLE instance.instances(id BIGINT PRIMARY KEY,customer_id BIGINT,order_id BIGINT,product_name VARCHAR(64),deleted_at DATETIME NULL)",
		"INSERT INTO `order`.orders VALUES(101,201),(102,201),(103,202)",
		"INSERT INTO instance.instances VALUES(301,201,101,'first',NULL),(302,201,101,'second',NULL),(303,202,101,'transferred',NULL),(304,201,101,'deleted',NOW())",
		"INSERT INTO `order`.order_items VALUES(401,102,301),(402,102,301),(403,103,301)",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	links, err := orderInstanceLinks(db, []string{"101", "102", "103"})
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 3 {
		t.Fatalf("want two purchase links and one distinct renewal link, got %#v", links)
	}
	counts := map[string]int{}
	for _, link := range links {
		counts[link.OrderID]++
		if link.CustomerID != "201" || link.ID == "303" || link.ID == "304" {
			t.Fatalf("invalid ownership/deletion link: %#v", link)
		}
	}
	if counts["101"] != 2 || counts["102"] != 1 || counts["103"] != 0 {
		t.Fatalf("unexpected links: %#v", links)
	}
}
