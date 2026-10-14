package billing

import (
	"bytes"
	"math/big"
	"strings"
	"testing"
)

func TestStatementExactTotalsAndCSV(t *testing.T) {
	n, _ := new(big.Int).SetString("999999999999999999", 10)
	if got := decimalTotal(n); got != "9999999999999999.99" {
		t.Fatal(got)
	}
	var b bytes.Buffer
	s := Statement{Items: []StatementRow{{ID: "1", TypeLabel: "人工增加", Amount: "10.00", Description: "  =HYPERLINK(\"https://invalid\")"}, {ID: "2", Amount: "-0.01", Description: "<script>alert(1)</script>\nnext"}}}
	if e := WriteStatementCSV(&b, s); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(b.String(), "'  =HYPERLINK") || !strings.Contains(b.String(), "'-0.01") {
		t.Fatal("CSV formula not escaped", b.String())
	}
	row := statementRow(Transaction{Type: "future_plugin", Amount: 10})
	if row.TypeLabel != "其他余额变动" || row.Description != "其他余额变动" {
		t.Fatal("unknown row hidden", row)
	}
}
