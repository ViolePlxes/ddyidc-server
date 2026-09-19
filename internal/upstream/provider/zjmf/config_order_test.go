package zjmf

import (
	"encoding/json"
	"testing"
)

func TestGroupedConfigValuesAreStable(t *testing.T) {
	raw := json.RawMessage(`{"z":{"child":[{"id":2,"name":"Z"}]},"a":{"child":[{"id":1,"name":"A"}]}}`)
	for i := 0; i < 100; i++ {
		values := configValues(raw)
		if len(values) != 2 || values[0].ID != 1 || values[1].ID != 2 {
			t.Fatal("map iteration must not change options or defaults")
		}
	}
}
