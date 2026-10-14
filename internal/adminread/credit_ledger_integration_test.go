package adminread

import (
	balance "ddyidc/internal/billing/balance"
	"encoding/json"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreditLedgerSnapshotDirectionAndTimeOrder(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL integration test")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") || filepath.Clean(socket) != socket {
		t.Fatal("isolated socket required")
	}
	db, err := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true&loc=Local"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	id := time.Now().UnixNano() / 1000
	db = db.Begin()
	if db.Error != nil {
		t.Fatal(db.Error)
	}
	defer db.Rollback()
	base := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	for i := int64(1); i <= 46; i++ {
		amount := balance.Money(100)
		typ := "manual_credit"
		if i%2 == 0 {
			amount = -100
			typ = "manual_debit"
		}
		row := balance.Transaction{ID: id + i, TxNo: fmt.Sprintf("CREDIT-READ-%d-%d", id, i), CustomerID: id, AccountID: id, AccountVersion: i, Type: typ, Direction: 1, Amount: amount, CreatedAt: base.Add(time.Duration(47-i) * time.Minute), UpdatedAt: base, ClientNotes: "synthetic prefetch test"}
		if err = db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	h := handler{db: db}
	type page struct {
		List     []balance.Transaction `json:"list"`
		Count    int64                 `json:"count"`
		Snapshot int64                 `json:"snapshot_version"`
	}
	read := func(params string) page {
		t.Helper()
		w := httptest.NewRecorder()
		h.creditList(w, httptest.NewRequest("GET", "/?limit=20&"+params, nil), id, false)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var envelope struct {
			Data page `json:"data"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	all := read("")
	if all.Count != 46 || len(all.List) != 20 || all.Snapshot != 46 || all.List[0].AccountVersion != 1 {
		t.Fatalf("latest-time-first or page failed: %+v", all)
	}
	extra := balance.Transaction{ID: id + 100, TxNo: fmt.Sprintf("CREDIT-READ-%d-NEW", id), CustomerID: id, AccountID: id, AccountVersion: 47, Type: "manual_credit", Amount: 100, CreatedAt: base.Add(24 * time.Hour), UpdatedAt: base}
	if err = db.Create(&extra).Error; err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"increase", "decrease"} {
		p := read("direction=" + dir + "&snapshotVersion=46")
		if p.Count != 23 || len(p.List) != 20 {
			t.Fatalf("direction short page: %s %+v", dir, p)
		}
		for _, row := range p.List {
			if (dir == "increase" && row.Amount <= 0) || (dir == "decrease" && row.Amount >= 0) || row.AccountVersion > 46 {
				t.Fatal("wrong direction or mixed snapshot")
			}
		}
		second := read("direction=" + dir + "&snapshotVersion=46&page=2")
		if len(second.List) != 3 {
			t.Fatal("second page incomplete")
		}
	}
	if read("").List[0].AccountVersion != 47 {
		t.Fatal("refresh did not include latest")
	}
	if read("keywords=synthetic").Count != 47-1 {
		t.Fatal("client reason search failed")
	}
	for _, params := range []string{"direction=bad", "snapshotVersion=-1", "snapshotVersion=999"} {
		w := httptest.NewRecorder()
		h.creditList(w, httptest.NewRequest("GET", "/?"+params, nil), id, false)
		if w.Code != 400 {
			t.Fatal("invalid query accepted")
		}
	}
}
