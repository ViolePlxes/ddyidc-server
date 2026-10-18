package order

import (
	"errors"
	"fmt"
	mysql "github.com/go-sql-driver/mysql"
	"log"
)

func orderCreationMessage(err error, requestID string) string {
	var dbError *mysql.MySQLError
	if !errors.As(err, &dbError) {
		return err.Error()
	}
	// Log the error class/code, never SQL, credentials or customer values.
	log.Printf("order_create_database_error request_id=%q mysql_code=%d", requestID, dbError.Number)
	if requestID == "" {
		return "创建订单暂不可用，请稍后重试"
	}
	return fmt.Sprintf("创建订单暂不可用，请稍后重试（请求编号：%s）", requestID)
}
