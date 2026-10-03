package customer

import (
	"encoding/json"
	"testing"
)

func TestPublicHistoryUsesOpaqueSystemIDs(t *testing.T) {
	order := int64(432109876543)
	rows := []legacyHistoryRow{{ID: 987654321012, SourceID: 71, Kind: "balance", OrderID: &order, Payload: `{"order_id":123,"client_notes":"客户备注","notes":"内部备注","client_id":456}`}, {ID: 123456789012, SourceID: 72, Kind: "payment", Payload: `{"order_id":999,"transaction_number":"original-bank-reference"}`}}
	publicHistoryRows(rows)
	if rows[0].SourceID != rows[0].ID || rows[0].Data["order_id"] != "432109876543" {
		t.Fatal("legacy sequential identifiers exposed")
	}
	if rows[1].Data["order_id"] != "0" || rows[1].Data["transaction_number"] != "original-bank-reference" {
		t.Fatal("excluded association or business reference changed")
	}
	if _, ok := rows[0].Data["notes"]; ok {
		t.Fatal("internal notes exposed")
	}
	if _, ok := rows[0].Data["client_id"]; ok {
		t.Fatal("legacy ownership ID exposed")
	}
	data, e := json.Marshal(rows)
	if e != nil {
		t.Fatal(e)
	}
	var got []map[string]any
	if json.Unmarshal(data, &got) != nil || got[0]["id"] != "987654321012" || got[0]["sourceId"] != "987654321012" {
		t.Fatal("opaque identifiers must be strings")
	}
}
