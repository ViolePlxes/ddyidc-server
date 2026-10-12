package customer

import "testing"

func TestHistoryPublicIDsUseCurrentIDs(t *testing.T) {
	rows := []legacyHistoryRow{{ID: 9007199254740993123, SourceID: 19, Kind: "ticket", Payload: `{"title":"fixture","ticket_num":"19"}`}, {ID: 9007199254740993555, SourceID: 20, Kind: "balance", Payload: `{"order_id":21}`}}
	publicHistoryRows(rows)
	for _, row := range rows {
		if row.SourceID != row.ID {
			t.Fatal("old source ID exposed")
		}
	}
	if rows[1].Data["order_id"] != "0" {
		t.Fatal("unmapped old order ID exposed")
	}
}
