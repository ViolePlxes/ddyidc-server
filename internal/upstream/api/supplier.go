package api
import ("net/http"; "strings"; "ddyidc/internal/upstream/service")
func(h *Handler)listSuppliers(w http.ResponseWriter,r *http.Request){respond(w,h.Store.ListSuppliers(),nil)}
func(h *Handler)createSupplier(w http.ResponseWriter,r *http.Request){var in service.SupplierInput;if e:=decode(r,&in);e!=nil{respond(w,nil,e);return};v,e:=h.Store.CreateSupplier(in);v.Token="";v.Secret="";respond(w,v,e)}
func(h *Handler)updateSupplier(w http.ResponseWriter,r *http.Request){var in service.SupplierInput;if e:=decode(r,&in);e!=nil{respond(w,nil,e);return};key:=strings.Split(id(r),"/")[0];v,e:=h.Store.UpdateSupplier(key,in);respond(w,v,e)}
func(h *Handler)deleteSupplier(w http.ResponseWriter,r *http.Request){respond(w,map[string]bool{"deleted":true},h.Store.DeleteSupplier(strings.Split(id(r),"/")[0]))}
func(h *Handler)testSupplier(w http.ResponseWriter,r *http.Request){key:=strings.TrimSuffix(id(r),"/test");respond(w,map[string]string{"status":"connected"},h.Store.TestSupplier(key))}
