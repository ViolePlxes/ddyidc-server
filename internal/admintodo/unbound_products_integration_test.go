package admintodo

import (
	"ddyidc/internal/shared/testenv"
	"ddyidc/pkg/auth"
	"github.com/zeromicro/go-zero/rest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnboundDeliveryRejectsOtherItemMySQL(t *testing.T) {
	tx := testenv.MySQL(t).Begin()
	defer tx.Rollback()
	for _, sql := range []string{
		"CREATE TEMPORARY TABLE `order`.orders(id BIGINT,customer_id BIGINT,status INT)",
		"CREATE TEMPORARY TABLE `order`.order_items(id BIGINT,order_id BIGINT)",
		"CREATE TEMPORARY TABLE system.durable_tasks(id BIGINT,kind VARCHAR(32),payload JSON,attempt INT,status VARCHAR(32))",
		"INSERT INTO `order`.orders VALUES(91001,99001,2),(91002,99002,2)",
		"INSERT INTO `order`.order_items VALUES(92001,91001),(92002,91002)",
		"INSERT INTO system.durable_tasks VALUES(93001,'post_payment','{\"customerId\":99002,\"businessId\":91002,\"businessType\":\"order\"}',1,'unknown'),(93002,'post_payment','{\"customerId\":99001,\"businessId\":91002,\"businessType\":\"order\"}',1,'unknown')",
	} {
		if e := tx.Exec(sql).Error; e != nil {
			t.Fatal(e)
		}
	}
	conf := rest.RestConf{}
	conf.Name = "unbound-delivery-ownership"
	conf.Host = "127.0.0.1"
	conf.Port = 0
	server := rest.MustNewServer(conf)
	registerUnboundProductWorkflows(server, tx, func(http.ResponseWriter, *http.Request) (*auth.Claims, bool) {
		return &auth.Claims{UserID: 1, Role: "admin"}, true
	}, unusedWorkflow{})
	for _, id := range []string{"93001", "93002"} {
		request := httptest.NewRequest("POST", "/admin/v1/todos/order-items/92001/tasks/"+id+"/review", strings.NewReader(`{"action":"reconcile","reason":"isolated ownership check","remoteInstanceId":"55036","expectedAttempt":1}`))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != 409 && response.Code != 403 {
			t.Fatalf("foreign order/customer task accepted: %d %s", response.Code, response.Body.String())
		}
	}
}
