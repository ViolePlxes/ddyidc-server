package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	repo "ddyidc/internal/instance/repository"
	"ddyidc/internal/shared/snowflake"
	upp "ddyidc/internal/upstream/provider"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/internal/upstream/provider/zjmf"
	"ddyidc/pkg/outbound"
	"encoding/json"
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type securityResolver struct{ provider upp.UpstreamProvider }

func (r securityResolver) ProviderFor(string) (upp.UpstreamProvider, error) { return r.provider, nil }
func (r securityResolver) GetOrderPageByLocalProduct(string) (*contracts.OrderPage, error) {
	return nil, ErrUnsupported
}

func TestSecurityOwnershipAndIdempotencyMySQL(t *testing.T) {
	socket := os.Getenv("FOUNDATION_MYSQL_SOCKET")
	if socket == "" {
		t.Skip("isolated MySQL required")
	}
	if !strings.HasPrefix(socket, "/tmp/ddyidc-foundation-") || !strings.HasSuffix(socket, "/mysql.sock") {
		t.Fatal("unsafe socket")
	}
	db, e := gorm.Open(mysql.Open("root@unix("+socket+")/?parseTime=true"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		t.Fatal(e)
	}
	pool, _ := db.DB()
	defer pool.Close()
	ctx := context.Background()
	if e = snowflake.Configure(ctx, pool); e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	name := ""
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/host/header":
			fmt.Fprint(w, `{"status":200,"data":{"host_data":{"id":42,"domainstatus":"Active"}}}`)
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"isolated-security-token"}`)
		case "/zjmf_api/provision/custom/content":
			if r.Form.Get("id") != "42" {
				t.Error("wrong instance")
			}
			mu.Lock()
			n := name
			mu.Unlock()
			source := `<table><tr><td>external-other-user</td><td></td><td><button class="apply" data-id="978">应用</button></td></tr>`
			if n != "" {
				source += fmt.Sprintf(`<tr><td>%s</td><td></td><td><button class="apply" data-id="7777">应用</button></td></tr>`, n)
			}
			source += "</table>"
			_ = json.NewEncoder(w).Encode(map[string]any{"status": 200, "data": map[string]string{"html": source}})
		case "/provision/custom/42":
			if r.Form.Get("func") == "showSecurityRules" {
				if r.Form.Get("id") != "7777" {
					t.Error("read external group")
				}
				fmt.Fprint(w, `{"status":200,"list":[{"id":123,"direction":"in","protocol":"tcp","port":"22","ip":"192.0.2.0/24"}]}`)
				return
			}
			writes.Add(1)
			if r.Form.Get("func") == "createSecurityGroup" {
				mu.Lock()
				name = r.Form.Get("name")
				mu.Unlock()
			} else if r.Form.Get("id") != "7777" {
				t.Error("mutated external group")
			}
			fmt.Fprint(w, `{"status":200}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	port := server.Listener.Addr().(*net.TCPAddr).Port
	if e = outbound.Configure(outbound.Config{PrivateCIDRs: []string{"127.0.0.1/32"}, Ports: []int{port}}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = outbound.Configure(outbound.Config{}) })
	supplier := int64(77001)
	h := dom.Host{ID: 7700101, InstanceNo: "SECURITY-ISOLATED-7700101", CustomerID: 7700100, UpstreamProviderID: &supplier, UpstreamInstanceID: "42", ModuleType: "dcimcloud", Status: 3, ConfigSnapshot: "{}"}
	if e = db.Create(&h).Error; e != nil {
		t.Fatal(e)
	}
	s := NewService(repo.NewRepository(db), securityResolver{zjmf.New(contracts.Supplier{URL: server.URL, Username: "isolated", Token: "isolated"})})
	s.SetManagement(db)
	input := SecurityInput{Action: "create", RequestID: "isolated-security-create-7700101", Name: "dedicated test"}
	if e = s.SecurityOperation(ctx, 7700101, h.ID, input); e != ErrForbidden {
		t.Fatalf("wrong owner accepted: %v", e)
	}
	if writes.Load() != 0 {
		t.Fatal("wrong owner reached provider")
	}
	if e = s.SecurityOperation(ctx, h.CustomerID, h.ID, input); e != nil {
		t.Fatal(e)
	}
	if e = s.SecurityOperation(ctx, h.CustomerID, h.ID, input); e == nil || writes.Load() != 1 {
		t.Fatal("duplicate request wrote remotely")
	}
	groups, e := s.SecurityGroups(ctx, h.CustomerID, h.ID)
	if e != nil || len(groups) != 1 || groups[0].Name != "dedicated test" || len(groups[0].Rules) != 1 {
		t.Fatalf("shared group leaked or rules lost: %+v %v", groups, e)
	}

	if e = db.Model(&SecurityGroupRecord{}).Where("id=?", groups[0].ID).Updates(map[string]any{"state": "unknown", "remote_id": ""}).Error; e != nil {
		t.Fatal(e)
	}
	if e = db.Table("instance.instance_security_commands").Where("request_id=?", input.RequestID).Update("state", "unknown").Error; e != nil {
		t.Fatal(e)
	}
	groups, e = s.SecurityGroups(ctx, h.CustomerID, h.ID)
	if e != nil || len(groups) != 1 || groups[0].State != "active" || writes.Load() != 1 {
		t.Fatal("read-only marker reconciliation failed or repeated remote write", e)
	}
	if e = s.SecurityOperation(ctx, h.CustomerID, h.ID, SecurityInput{Action: "apply", GroupID: "978", RequestID: "isolated-external-apply-7700101"}); e == nil || writes.Load() != 1 {
		t.Fatal("external group mutable")
	}
	rule := contracts.SecurityRule{Direction: "in", Protocol: "tcp", Port: "22", IP: "192.0.2.0/24"}
	if e = s.SecurityOperation(ctx, h.CustomerID, h.ID, SecurityInput{Action: "add_rule", GroupID: groups[0].ID, RequestID: "isolated-rule-add-7700101", Rule: rule}); e != nil {
		t.Fatal(e)
	}
	if writes.Load() != 2 {
		t.Fatal("rule mutation count wrong")
	}
	if e = db.Model(&dom.Host{}).Where("id=?", h.ID).Updates(map[string]any{"customer_id": 7700102, "ownership_version": 1}).Error; e != nil {
		t.Fatal(e)
	}
	if e = s.SecurityOperation(ctx, h.CustomerID, h.ID, SecurityInput{Action: "apply", GroupID: groups[0].ID, RequestID: "isolated-after-transfer-7700101"}); e != ErrForbidden || writes.Load() != 2 {
		t.Fatal("old owner retained access after transfer")
	}
	t.Log("real isolated MySQL + controlled HTTP: external shared groups hidden, wrong-owner/old-owner writes denied, duplicate remote writes blocked, own rules read and mutated")
}
