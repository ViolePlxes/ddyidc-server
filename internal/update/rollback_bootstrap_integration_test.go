package update

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"testing"
)

func TestPristineCDNBootstrapMySQL(t *testing.T) {
	database := testenv.MySQL(t)
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = database.Exec("INSERT INTO system.update_tasks(id,target,status,branch,trigger_by,log,error) VALUES(930001,'backend','running','main',930001,'','')").Error; err != nil {
		t.Fatal(err)
	}
	defer database.Exec("UPDATE system.update_tasks SET status='success' WHERE id=930001")
	if err = VerifyPristineCDNBootstrap(ctx, pool); err != nil {
		t.Fatal("fresh disabled module rejected", err)
	}
	if err = database.Exec("INSERT INTO system.cdnsell_products(id,name,capacity_bytes,valid_days,price,tier_prices,description) VALUES(930001,'must-not-insert',1024,30,1.00,'{}','')").Error; err == nil {
		t.Fatal("concurrent product insert bypassed bootstrap guard")
	}
	if err = database.Exec("UPDATE system.cdnsell_settings SET enabled=1 WHERE id=1").Error; err == nil {
		t.Fatal("concurrent enabling bypassed bootstrap guard")
	}
	var count int64
	database.Table("system.cdnsell_products").Count(&count)
	if count != 0 {
		t.Fatal("guard left product data")
	}
	if err = database.Exec("UPDATE system.update_tasks SET status='failed' WHERE id=930001").Error; err != nil {
		t.Fatal(err)
	}
	if err = database.Exec("UPDATE system.cdnsell_settings SET enabled=1 WHERE id=1").Error; err == nil {
		t.Fatal("failed legacy recovery enabled CDN")
	}
	if err = database.Exec("INSERT INTO system.cdnsell_products(id,name,capacity_bytes,valid_days,price,tier_prices,description) VALUES(930001,'must-not-insert-after-failure',1024,30,1.00,'{}','')").Error; err == nil {
		t.Fatal("failed legacy recovery created CDN business data")
	}
	if err = database.Exec("UPDATE system.update_tasks SET status='success' WHERE id=930001").Error; err != nil {
		t.Fatal(err)
	}
	if err = VerifyPristineCDNBootstrap(ctx, pool); err == nil {
		t.Fatal("bootstrap allowed outside online update")
	}
	if err = database.Exec("INSERT INTO system.cdnsell_products(id,name,capacity_bytes,valid_days,price,tier_prices,description) VALUES(930001,'isolated-after-release',1024,30,1.00,'{}','')").Error; err != nil {
		t.Fatal("completed update still blocks normal configuration", err)
	}
	if err = database.Exec("UPDATE system.update_tasks SET status='running' WHERE id=930001").Error; err != nil {
		t.Fatal(err)
	}
	if err = VerifyPristineCDNBootstrap(ctx, pool); err == nil {
		t.Fatal("nonempty legacy exception accepted")
	}
	// No deletion or financial rollback: preserve the fixture and its audit.
	t.Log("empty disabled bootstrap accepted; concurrent insert/enable denied atomically; completed update releases guard; any business row blocks legacy rollback")
}
