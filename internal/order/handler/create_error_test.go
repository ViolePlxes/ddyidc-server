package order

import (
	"errors"
	"fmt"
	mysql "github.com/go-sql-driver/mysql"
	"strings"
	"testing"
)

func TestOrderCreationDatabaseErrorIsPrivate(t *testing.T) {
	e := fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1054, Message: "supplier_id private SQL customer-value"})
	message := orderCreationMessage(e, "server-generated-id")
	if !strings.Contains(message, "server-generated-id") || strings.Contains(message, "supplier_id") || strings.Contains(message, "customer-value") {
		t.Fatal(message)
	}
	if got := orderCreationMessage(errors.New("商品已下架"), "id"); got != "商品已下架" {
		t.Fatal(got)
	}
}
