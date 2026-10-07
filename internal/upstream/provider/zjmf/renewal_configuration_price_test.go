package zjmf

import (
	"encoding/json"
	"testing"
)

func TestRenewalFullConfigurationAndFixedContract(t *testing.T) {
	var data map[string]any
	json.Unmarshal([]byte(`{"host":{"flag":1,"billingcycle":"monthly"},"cycle":[{"billingcycle":"monthly","price":"10.00","amount":"17.00","saleproducts":"0.00"},{"billingcycle":"quarterly","price":"30.00","amount":"51.00","saleproducts":"9.00"},{"billingcycle":"semiannually","price":"60.00","amount":"102.00","saleproducts":"18.00"},{"billingcycle":"annually","price":"120.00","amount":"204.00","saleproducts":"36.00"}]}`), &data)
	rows := data["cycle"].([]any)
	host := data["host"].(map[string]any)
	for i, want := range []string{"20.00", "60.00", "120.00", "240.00"} {
		got, e := renewalOriginalAmount(rows[i].(map[string]any), rows, host)
		if e != nil || got.String() != want {
			t.Fatalf("cycle %d: got %s err %v", i, got.String(), e)
		}
	}
	rows[2].(map[string]any)["saleproducts"] = "10.00"
	if _, e := renewalOriginalAmount(rows[0].(map[string]any), rows, host); e == nil {
		t.Fatal("inconsistent discount evidence must not produce a price")
	}
	rows = rows[:2]
	if _, e := renewalOriginalAmount(rows[0].(map[string]any), rows, host); e == nil {
		t.Fatal("one reference cycle cannot establish original contract price")
	}
}
