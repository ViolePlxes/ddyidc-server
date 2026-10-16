package notify

import (
	"context"
	"errors"
	dysms "github.com/alibabacloud-go/dysmsapi-20170525/v5/client"
	"github.com/alibabacloud-go/tea/dara"
	"strings"
	"time"
)

func (c *aliyunSMSChannel) QueryReceipt(ctx context.Context, config SMSConfig, mobile, bizID string, sentAt time.Time) (SMSReceipt, error) {
	client, e := c.clientFor(config)
	if e != nil {
		return SMSReceipt{}, errors.New("短信查询配置无效")
	}
	for _, date := range receiptQueryDates(sentAt) {
		request := (&dysms.QuerySendDetailsRequest{}).SetPhoneNumber(mobile).SetBizId(bizID).SetSendDate(date).SetPageSize(50).SetCurrentPage(1)
		response, e := client.QuerySendDetailsWithContext(ctx, request, &dara.RuntimeOptions{})
		if e != nil || response == nil || response.Body == nil || stringValue(response.Body.Code) != "OK" {
			return SMSReceipt{}, errors.New("短信回执查询暂不可用")
		}
		body := response.Body
		if stringValue(body.TotalCount) == "0" {
			continue
		}
		if body.SmsSendDetailDTOs == nil || len(body.SmsSendDetailDTOs.SmsSendDetailDTO) != 1 || stringValue(body.TotalCount) != "1" {
			return SMSReceipt{Status: "receipt_unknown", Code: "NO_UNIQUE_RECEIPT"}, nil
		}
		row := body.SmsSendDetailDTOs.SmsSendDetailDTO[0]
		if row == nil || row.SendStatus == nil {
			return SMSReceipt{Status: "receipt_unknown", Code: "INVALID_RECEIPT"}, nil
		}
		return classifySMSReceipt(mobile, sentAt, stringValue(row.PhoneNum), stringValue(row.SendDate), stringValue(row.ReceiveDate), stringValue(row.ErrCode), int(*row.SendStatus)), nil
	}
	return SMSReceipt{Status: "receipt_pending", Code: "NO_RECEIPT_YET"}, nil
}
func receiptQueryDates(sentAt time.Time) []string {
	local := sentAt.In(time.FixedZone("Asia/Shanghai", 8*3600))
	dates := []string{local.Format("20060102")}
	// The send response/log can cross midnight; only try the preceding date near midnight.
	if local.Hour() == 0 && local.Minute() < 10 {
		dates = append(dates, local.AddDate(0, 0, -1).Format("20060102"))
	}
	return dates
}

func classifySMSReceipt(mobile string, sentAt time.Time, phone, sendDate, receiveDate, code string, status int) SMSReceipt {
	unknown := SMSReceipt{Status: "receipt_unknown", Code: "RECEIPT_MISMATCH"}
	if strings.TrimSpace(phone) != mobile && strings.TrimSpace(phone) != maskTarget(mobile) {
		return unknown
	}
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	sent, e := time.ParseInLocation("2006-01-02 15:04:05", sendDate, zone)
	if e != nil || sent.Sub(sentAt) > 10*time.Minute || sentAt.Sub(sent) > 10*time.Minute {
		return unknown
	}
	switch status {
	case 1:
		return SMSReceipt{Status: "receipt_pending", Code: "WAITING_RECEIPT"}
	case 2:
		if code == "" || code == "DELIVERED" {
			return unknown
		}
		return SMSReceipt{Status: "delivery_failed", Code: safeReceiptCode(code)}
	case 3:
		received, e := time.ParseInLocation("2006-01-02 15:04:05", receiveDate, zone)
		if code != "DELIVERED" || e != nil || received.Before(sent) {
			return unknown
		}
		received = received.UTC()
		return SMSReceipt{Status: "delivered", Code: code, ReceivedAt: &received}
	default:
		return unknown
	}
}
