package cdnsell

import (
	"encoding/json"
	"testing"
)

func TestYuanBoundaries(t *testing.T) {
	for _, input := range []string{`"19.99"`, `19.99`, `19.990000`} {
		var v Yuan
		if err := json.Unmarshal([]byte(input), &v); err != nil || v != 1999 {
			t.Fatal(input, v, err)
		}
		db, _ := v.Value()
		if db != "19.99" {
			t.Fatal(db)
		}
		b, _ := json.Marshal(v)
		if string(b) != `"19.99"` {
			t.Fatal(string(b))
		}
	}
	for _, input := range []string{`1.999`, `1e2`, `null`, `"1000000000000.00"`} {
		var v Yuan
		if json.Unmarshal([]byte(input), &v) == nil {
			t.Fatal("invalid accepted", input)
		}
	}
	amount, err := overageAmount(1<<30, 199)
	if err != nil || amount != 199 {
		t.Fatal(amount, err)
	}
	amount, err = overageAmount(1, 199)
	if err != nil || amount != 1 {
		t.Fatal("fractional byte charge not rounded to cent", amount, err)
	}
	if _, err = overageAmount(9223372036854775807, 99999999999999); err == nil {
		t.Fatal("overflow accepted")
	}
}
