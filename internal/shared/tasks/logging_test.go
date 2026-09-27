package tasks

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestTaskLogDoesNotIncludePayloadOrLease(t *testing.T) {
	var out bytes.Buffer
	old := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(old)
	logTask(&Task{ID: 1, Kind: "post_payment", Attempt: 1, LeaseToken: "LEASE_SECRET", Payload: `{"orderId":42,"businessId":43,"password":"PAYLOAD_SECRET","token":"AUTH_SECRET"}`}, "unknown", 25)
	value := out.String()
	for _, secret := range []string{"LEASE_SECRET", "PAYLOAD_SECRET", "AUTH_SECRET", "password", "token"} {
		if strings.Contains(value, secret) {
			t.Fatalf("sensitive field logged: %s", secret)
		}
	}
	for _, expected := range []string{`"task_id":"1"`, `"order_id":"42"`, `"business_id":"43"`, `"result":"unknown"`} {
		if !strings.Contains(value, expected) {
			t.Fatalf("missing %s", expected)
		}
	}
}
