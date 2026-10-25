package domains

import (
 "context"
 "encoding/json"
 "errors"
 "fmt"
 "net/http"
 "strconv"
 "strings"
 "sync"
 "sync/atomic"
 "testing"
 "time"

 balance "ddyidc/internal/billing/balance"
 od "ddyidc/internal/order/domain"
 "ddyidc/internal/shared/testenv"
 "gorm.io/gorm"
)

func TestDomainLifecycleMySQL(t *testing.T){
 database:=testenv.MySQL(t);ctx:=context.Background();key:=[]byte("0123456789abcdef0123456789abcdef");s:=New(database,key);now:=time.Now().UTC().Truncate(time.Second);s.now=func()time.Time{return now}
 must:=func(e error){t.Helper();if e!=nil{t.Fatal(e)}}
 for _,id:=range []int64{95001001,95001002}{must(database.Exec("INSERT INTO customer.customers(id,username,password_hash,status) VALUES(?,?,'disabled',1)",id,fmt.Sprint("domain-fixture-",id)).Error)}
 must(database.Exec("INSERT INTO system.admins(id,username,password_hash,real_name,status,is_super) VALUES(95001099,'domain-fixture-admin','disabled','isolated',1,1)").Error)
 cfg,e:=s.Settings(ctx,false);must(e);cfg.Enabled=true;cfg.Username="isolated-agent";cfg.Password="isolated-api-secret";cfg.Notes="isolated configuration";must(s.SaveSettings(ctx,95001099,"127.0.0.1",cfg))
 var calls atomic.Int64;var writes atomic.Int64;var timeout atomic.Bool;var registrationRejected atomic.Bool;var mu sync.Mutex
 expiry:=map[string]time.Time{"owned.example":now.AddDate(1,0,0)}
 s.provider=testClient(func(r *http.Request)(*http.Response,error){calls.Add(1);_ = r.ParseForm();action:=r.Form.Get("act");domain:=r.Form.Get("domain");output:=map[string]any{"result":200,"data":map[string]any{}}
  switch{
  case strings.HasSuffix(r.URL.Path,"/domain/query/"):output["data"]=[]map[string]any{{"name":domain+r.Form.Get("suffix"),"avail":1}}
  case action=="getprice":output["data"]=map[string]any{"buyprice":"10.00","renewprice":"20.00","buyyear":1}
  case action=="getrenprice":output["data"]=map[string]any{"price":"30.00","year":r.Form.Get("year")}
  case action=="domaininfo":mu.Lock();date:=expiry[domain];mu.Unlock();if date.IsZero(){date=now.AddDate(1,0,0)};output["data"]=map[string]any{"domain":domain,"rexpiredate":date.Format("2006-01-02 15:04:05")}
  case action=="getdnsrecord":output["data"]=map[string]any{"items":[]map[string]any{{"id":42,"host":"@","value":"127.0.0.1"}}}
  case action=="renew":writes.Add(1);mu.Lock();old:=expiry[domain];years,_:=strconv.Atoi(r.Form.Get("year"));expiry[domain]=anniversary(old,years);mu.Unlock()
  case action=="regdomain":writes.Add(1);if registrationRejected.Load(){output["data"]=map[string]any{domain:"not available"}}else{output["data"]=map[string]any{domain:200};mu.Lock();expiry[domain]=now.AddDate(1,0,0);mu.Unlock()}
  case action=="adddnsrecord"||action=="deldnsrecord":writes.Add(1);if timeout.Load(){return nil,errors.New("simulated acknowledgement loss")};output["data"]=map[string]any{"id":43}
  case action=="auditinfo":output["data"]=map[string]any{"c_sysid":r.Form.Get("c_sysid"),"c_status":1}
  case action=="dnvdellogs":output["data"]=[]map[string]any{{"domain":"owned.example"},{"domain":"foreign.example"}}
  }
  raw,_:=json.Marshal(output);return response(string(raw)),nil
 })
 template:=Template{ID:95001101,CustomerID:95001001,ProviderID:"73001",Label:"本人模板",Status:"submitted",CreatedAt:now,UpdatedAt:now};must(database.Create(&template).Error)
 other:=Template{ID:95001102,CustomerID:95001002,ProviderID:"73002",Label:"其他模板",Status:"submitted",CreatedAt:now,UpdatedAt:now};must(database.Create(&other).Error)
 must(database.Create(&Asset{ID:95001103,CustomerID:95001001,Domain:"owned.example",Status:"active",CreatedAt:now,UpdatedAt:now}).Error)
 t.Run("secrets-and-isolation",func(t *testing.T){
  public,e:=s.Settings(ctx,false);must(e);raw,_:=json.Marshal(public);if strings.Contains(string(raw),"isolated-api-secret")||public.Password!=""{t.Fatal("credential exposed")};var stored string;must(database.Table("system.domain_settings").Select("password_cipher").Where("id=1").Scan(&stored).Error);if stored=="isolated-api-secret"||stored==""{t.Fatal("credential not encrypted")}
  before:=calls.Load();for _,req:=range []ActionRequest{{Action:"4.8",Params:map[string]string{"domain":"foreign.example"}},{Action:"4.5",Params:map[string]string{"c_sysid":"95001102"}}}{if _,e:=s.Execute(ctx,95001001,req);!errors.Is(e,ErrOwnership){t.Fatal("foreign asset accepted",e)}};if calls.Load()!=before{t.Fatal("unauthorized request reached provider")}
  list,e:=s.Execute(ctx,95001001,ActionRequest{Action:"3.3"});must(e);if len(list.Data.([]Asset))!=1{t.Fatal("customer domain isolation failed")}
  _,e=s.Execute(ctx,95001001,ActionRequest{Action:"3.9",Confirmed:true,RequestID:"delete-foreign-record-id",Params:map[string]string{"domain":"owned.example","id":"999"}});if !errors.Is(e,ErrOwnership){t.Fatal("foreign DNS record accepted",e)}
  result,e:=s.Execute(ctx,95001001,ActionRequest{Action:"3.36"});must(e);if len(result.Data.([]any))!=1{t.Fatal("registrar-global logs leaked")}
 })
 t.Run("concurrent-write-and-replay",func(t *testing.T){
  request:=ActionRequest{Action:"3.7",Confirmed:true,RequestID:"same-dns-write-request-01",Params:map[string]string{"domain":"owned.example","host":"@","type":"A","value":"127.0.0.1","ttl":"900","level":"10"}};before:=writes.Load();var wg sync.WaitGroup
  for i:=0;i<8;i++{wg.Add(1);go func(){defer wg.Done();if _,e:=s.Execute(ctx,95001001,request);e!=nil{t.Error(e)}}()};wg.Wait();if writes.Load()!=before+1{t.Fatal("DNS command replayed",writes.Load()-before)}
  request.Params["value"]="127.0.0.2";if _,e:=s.Execute(ctx,95001001,request);e==nil{t.Fatal("request identity conflict accepted")}
 })
 t.Run("unknown-write-never-retried",func(t *testing.T){
  timeout.Store(true);before:=writes.Load();request:=ActionRequest{Action:"3.9",Confirmed:true,RequestID:"unknown-dns-write-request",Params:map[string]string{"domain":"owned.example","id":"42"}}
  if _,e:=s.Execute(ctx,95001001,request);!errors.Is(e,ErrPending){t.Fatal("uncertain response not identified",e)};request.RequestID="another-unknown-dns-request";if _,e:=s.Execute(ctx,95001001,request);!errors.Is(e,ErrPending){t.Fatal("uncertain operation repeated",e)};timeout.Store(false);if writes.Load()!=before+1{t.Fatal("unknown write repeated")}
 })
 var renewalID int64
 t.Run("unpaid-and-insufficient-balance",func(t *testing.T){
  q,e:=s.Quote(ctx,95001001,ActionRequest{Action:"3.2",Params:map[string]string{"domain":"owned.example","year":"1"}});must(e);p,e:=s.CreateOrder(ctx,95001001,q.ID,true);must(e);renewalID,_=strconv.ParseInt(p.OrderID,10,64)
  before:=writes.Load();if _,e=s.ProvisionPlatformOrder(ctx,95001001,renewalID,"balance");e==nil{t.Fatal("unpaid procurement accepted")};if writes.Load()!=before{t.Fatal("unpaid order called provider write")}
  if e=s.PayBalance(ctx,95001001,renewalID);!errors.Is(e,balance.ErrInsufficient){t.Fatal("insufficient balance not refused",e)};var debits int64;must(database.Table("billing.transactions").Where("customer_id=? AND type='order_pay'",95001001).Count(&debits).Error);if debits!=0{t.Fatal("insufficient account debited")}
 })
 t.Run("paid-renewal-exactly-once",func(t *testing.T){
  _,e:=balance.New(database).Change(ctx,balance.ChangeRequest{CustomerID:95001001,Type:"manual_credit",Amount:"100.00",RefType:"customer",RefID:95001001,Channel:"admin_manual",RequestID:"domain-test-initial-credit",AdminID:95001099,AdminName:"domain-fixture-admin",OperatorIP:"127.0.0.1",Notes:"isolated balance fixture",ClientNotes:"isolated fixture"});must(e)
  must(s.PayBalance(ctx,95001001,renewalID));must(s.PayBalance(ctx,95001001,renewalID));before:=writes.Load();matched,e:=s.ProvisionPlatformOrder(ctx,95001001,renewalID,"balance");must(e);if !matched{t.Fatal("module not matched")};_,e=s.ProvisionPlatformOrder(ctx,95001001,renewalID,"balance");must(e);if writes.Load()!=before+1{t.Fatal("renewal repeated")}
  var platform od.Order;must(database.Take(&platform,"id=?",renewalID).Error);if platform.Status!=od.OrderStatusCompleted{t.Fatal("paid order not completed")};var account balance.Account;must(database.Take(&account,"customer_id=?",95001001).Error);if account.Balance!=7000{t.Fatal("incorrect balance",account.Balance)};var count int64;must(database.Table("billing.transactions").Where("customer_id=? AND type='order_pay'",95001001).Count(&count).Error);if count!=1{t.Fatal("duplicate balance debit")}
 })
 t.Run("quote-owner-expiry-and-registration-result",func(t *testing.T){
  req:=ActionRequest{Action:"4.6",Params:map[string]string{"domain":"registration.example","regyear":"1","c_sysid":"95001101"}};q,e:=s.Quote(ctx,95001001,req);must(e);if _,e=s.CreateOrder(ctx,95001002,q.ID,true);!errors.Is(e,ErrOwnership){t.Fatal("foreign quote accepted")};must(database.Model(q).Update("expires_at",now.Add(-time.Second)).Error);if _,e=s.CreateOrder(ctx,95001001,q.ID,true);e==nil{t.Fatal("expired quote accepted")}
  q,e=s.Quote(ctx,95001001,req);must(e);p,e:=s.CreateOrder(ctx,95001001,q.ID,true);must(e);id,_:=strconv.ParseInt(p.OrderID,10,64);must(s.PayBalance(ctx,95001001,id));registrationRejected.Store(true);before:=writes.Load();if _,e=s.ProvisionPlatformOrder(ctx,95001001,id,"balance");e==nil{t.Fatal("outer success hid failed per-domain result")};registrationRejected.Store(false);var row DomainOrder;must(database.Take(&row,"id=?",id).Error);if row.Status!="review"{t.Fatal("failure not retained",row.Status)};_,_=s.ProvisionPlatformOrder(ctx,95001001,id,"balance");if writes.Load()!=before+1{t.Fatal("failed registration retried")}
 })
 t.Run("maintenance-does-not-write-registrar",func(t *testing.T){
  before:=writes.Load();must(s.Maintain(ctx));if writes.Load()!=before{t.Fatal("maintenance replayed registrar writes")}
 })
 t.Run("runtime-identity-validation",func(t *testing.T){pool,_:=database.DB();if e:=ApplyRuntimePrivileges(ctx,pool,"untrusted","127.0.0.1");e==nil{t.Fatal("unexpected runtime identity accepted")}})
}

// Keep gorm available to fixture extensions that exercise transaction failures.
var _ *gorm.DB
