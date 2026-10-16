package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/utils"
	dysms "github.com/alibabacloud-go/dysmsapi-20170525/v5/client"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSMSReceiptSDKQueryContract(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = r.ParseForm()
		action := r.Form.Get("Action")
		if action == "" {
			action = r.Header.Get("x-acs-action")
		}
		if action != "QuerySendDetails" || r.Form.Get("PhoneNumber") != "19900000000" || r.Form.Get("BizId") != "fixture-biz" || r.Form.Get("SendDate") != "20261016" {
			t.Error("SDK query did not bind recipient, BizId and Shanghai date")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Code":"OK","TotalCount":"1","SmsSendDetailDTOs":{"SmsSendDetailDTO":[{"PhoneNum":"199****0000","SendDate":"2026-10-16 08:00:00","ReceiveDate":"2026-10-16 08:00:03","SendStatus":3,"ErrCode":"DELIVERED"}]}}`)
	}))
	defer server.Close()
	config := SMSConfig{AccessKey: "fixture-key", AccessSecret: "fixture-secret"}
	endpoint := strings.TrimPrefix(server.URL, "http://")
	protocol := "http"
	client, e := dysms.NewClient(&openapi.Config{AccessKeyId: &config.AccessKey, AccessKeySecret: &config.AccessSecret, Endpoint: &endpoint, Protocol: &protocol})
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256([]byte(config.AccessKey + "\x00" + config.AccessSecret))
	channel := &aliyunSMSChannel{client: client, configHash: hex.EncodeToString(digest[:])}
	result, e := channel.QueryReceipt(context.Background(), config, "19900000000", "fixture-biz", time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	if e != nil || result.Status != "delivered" || calls != 1 {
		t.Fatalf("SDK query contract failed: status=%s calls=%d error=%v", result.Status, calls, e)
	}
}
