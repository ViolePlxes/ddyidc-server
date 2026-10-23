package update

import (
 "context"
 "net/http/httptest"
 "strings"
 "testing"
 "ddyidc/pkg/auth"
)

func TestReclamationEndpointsRequireAdminAuthentication(t *testing.T) {
 h:=&adminHandler{auth:auth.NewManager(strings.Repeat("a",32),strings.Repeat("b",32),3600,3600)}
 for _,test:=range []struct{method,path string}{
  {"GET","/admin/v1/update/reclamation"},
  {"GET","/admin/v1/update/reclamation/history"},
  {"POST","/admin/v1/update/reclamation/preview"},
  {"POST","/admin/v1/update/reclamation/retry"},
 } {
  request:=httptest.NewRequest(test.method,test.path,strings.NewReader(`{"path":"/var/lib/mysql","force":true}`))
  response:=httptest.NewRecorder()
  if strings.HasSuffix(test.path,"/history"){h.reclamationHistory(response,request)}else if test.method=="GET"{h.reclamation(response,request)}else{h.retryReclamation(response,request)}
  if response.Code!=401{t.Fatalf("%s: unauthenticated cleanup accepted: %d",test.path,response.Code)}
 }
}

func TestReclamationPrivilegeIdentityRejectsInjection(t *testing.T) {
 for _,user:=range []string{"", "runtime'", "runtime\\x", "runtime name", "runtime\nname", "runtime\x00name"} {
  if err:=ApplyReclamationPrivileges(context.Background(),nil,user,"127.0.0.1");err==nil{t.Fatalf("invalid privileged identity accepted: %q",user)}
 }
}
