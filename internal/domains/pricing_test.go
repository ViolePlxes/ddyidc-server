package domains

import (
 "testing"
 "encoding/json"
 balance "ddyidc/internal/billing/balance"
)
func TestPricesUseExactYuanAndSingleServiceFee(t *testing.T){cfg:=Settings{MarkupBPS:1500,ServiceFee:100};price,e:=sellingPrice(3000,cfg);if e!=nil||price!=3550{t.Fatal("incorrect markup or unit",price,e)};if _,e:=sellingPrice(balance.MaxMoney,cfg);e==nil{t.Fatal("overflow accepted")};for _,raw:=range []string{"-1","0","1.001","1e3","9000000000.00"}{if _,e:=moneyValue(json.Number(raw));e==nil{t.Fatal("unsafe amount accepted",raw)}}
 s:=&Service{};r,e:=s.retailResult(mustOperation("1.1"),map[string]string{"year":"3"},map[string]any{"buyprice":json.Number("10.00"),"renewprice":json.Number("20.00")},Settings{MarkupBPS:1000,ServiceFee:100});if e!=nil{t.Fatal(e)};if r.Data.(map[string]any)["amount"]!="56.00"{t.Fatal("multi-year registration pricing incorrect",r.Data)}
 raw,_:=json.Marshal(r);if string(raw)==""{t.Fatal("no response")};if _,ok:=r.Data.(map[string]any)["buyprice"];ok{t.Fatal("wholesale cost exposed")}
}
func TestLeapDayAndServerOwnedPriceFields(t *testing.T){old,e:=domainTime("2028-02-29 12:34:56");if e!=nil{t.Fatal(e)};next:=anniversary(old,1);if next.Format("2006-01-02 15:04:05")!="2029-02-28 12:34:56"{t.Fatal("anniversary not clamped")};for _,extra:=range []string{"client_price","expiredate","token","price"}{_,e:=validateInput(mustOperation("3.2"),map[string]string{"domain":"example.com","year":"1",extra:"1"});if e==nil{t.Fatal("trusted quote field overridden",extra)}}
 if _,e:=validateInput(mustOperation("3.19"),map[string]string{"domain":"example.com","hostname":"ns.other.com","dnsip":"127.0.0.1"});e==nil{t.Fatal("foreign DNS host accepted")}
}
