package inventory

import (
	"context"
	"errors"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestStockReservationsMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	database, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	pool, _ := database.DB()
	defer pool.Close()
	if err := database.Exec("INSERT INTO product.products(id,group_id,name,module_type,stock) VALUES(95001,1,'isolated last stock','cloud',1),(95002,1,'isolated override','cloud',100)").Error; err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var success atomic.Int32
	var winning atomic.Int64
	for n := int64(0); n < 20; n++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			<-start
			err := database.Transaction(func(tx *gorm.DB) error { return Reserve(tx, id, 95001, 1) })
			if err == nil {
				success.Add(1)
				winning.Store(id)
			}
		}(96000 + n)
	}
	close(start)
	wg.Wait()
	if success.Load() != 1 {
		t.Fatal("last-stock oversold", success.Load())
	}
	for n := 0; n < 2; n++ {
		if err := database.Transaction(func(tx *gorm.DB) error { return Release(tx, winning.Load(), false) }); err != nil {
			t.Fatal(err)
		}
	}
	var stock int
	database.Raw("SELECT stock FROM product.products WHERE id=95001").Scan(&stock)
	if stock != 1 {
		t.Fatal("release duplicated", stock)
	}
	if err := database.Exec("INSERT INTO product.product_specs(id,product_id,name,vcpu,memory,stock) VALUES(95003,95002,'isolated',1,'1GB',2)").Error; err != nil {
		t.Fatal(err)
	}
	failure := errors.New("business rollback")
	err = database.Transaction(func(tx *gorm.DB) error {
		if err := Reserve(tx, 97001, 95002, 2); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	database.Raw("SELECT stock FROM product.product_specs WHERE id=95003").Scan(&stock)
	if stock != 2 {
		t.Fatal("reservation escaped rollback")
	}
	if err := database.Transaction(func(tx *gorm.DB) error {
		if err := Reserve(tx, 97002, 95002, 2); err != nil {
			return err
		}
		return Consume(tx, 97002)
	}); err != nil {
		t.Fatal(err)
	}
	database.Raw("SELECT stock FROM product.products WHERE id=95002").Scan(&stock)
	if stock != 100 {
		t.Fatal("wrong quota source")
	}
	if err := database.Transaction(func(tx *gorm.DB) error { return Release(tx, 97002, true) }); err != nil {
		t.Fatal(err)
	}
	database.Raw("SELECT stock FROM product.product_specs WHERE id=95003").Scan(&stock)
	if stock != 2 {
		t.Fatal("refund quota not restored")
	}
	if err := database.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error { return Consume(tx, 97002) }); !errors.Is(err, ErrReleased) {
		t.Fatal("released stock consumed", err)
	}
	t.Log("20-way last-stock race admits one order; release idempotent; business rollback atomic; spec override precedence; consumed refund releases once")
}
