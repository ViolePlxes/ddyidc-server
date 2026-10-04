package instance

import (
	"net/http/httptest"
	"testing"
)

func TestCSVFormulaAndPagination(t *testing.T) {
	for _, s := range []string{"=WEBSERVICE(\"https://invalid\")", "  +SUM(A1)", "\t@danger", "\r-1", "\x00=1"} {
		if csvCell(s)[0] != '\'' {
			t.Fatalf("unsafe CSV %q", s)
		}
	}
	for _, query := range []string{"page=-1", "page=nan", "pageSize=0"} {
		if _, e := adminListInput(httptest.NewRequest("GET", "/admin/v1/instances?"+query, nil)); e == nil {
			t.Fatal("invalid pagination", query)
		}
	}
}
