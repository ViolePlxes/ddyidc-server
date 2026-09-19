package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActualTransportCountsRedirectsAndFailures(t *testing.T) {
	Default = New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/fail", 302)
			return
		}
		w.WriteHeader(503)
	}))
	defer server.Close()
	client := http.Client{Transport: Transport{SupplierID: "test"}}
	response, err := client.Get(server.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if Default.Snapshot()["test"].OneMinute != 2 {
		t.Fatal("redirect/failure undercount")
	}
}
