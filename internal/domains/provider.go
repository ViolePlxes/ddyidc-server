package domains

import (
 "bytes"
 "context"
 "crypto/md5" // West v2 protocol signature, not password storage.
 "encoding/hex"
 "encoding/json"
 "errors"
 "io"
 "net/http"
 "net/url"
 "strconv"
 "strings"
 "time"
 "unicode/utf8"

 "ddyidc/pkg/outbound"
 "golang.org/x/text/encoding/simplifiedchinese"
 "golang.org/x/text/transform"
)

var ErrConnection=errors.New("域名服务连接失败，请稍后重试或联系客服")
var ErrCredentials=errors.New("域名服务尚未连接，请联系客服")
type ProviderResult struct { Result json.Number `json:"result"`; ClientID string `json:"clientid"`; Msg string `json:"msg"`; ErrCode json.Number `json:"errcode"`; Data json.RawMessage `json:"data"` }
type ProviderFailure struct { Code string }
func (e *ProviderFailure) Error()string{return "域名服务拒绝了请求，请核对填写的信息（"+e.Code+"）"}
type WestClient struct { HTTP *http.Client; Now func()time.Time }
func NewWestClient()*WestClient{h:=outbound.NewSingleAttemptClient(35*time.Second);h.CheckRedirect=func(*http.Request,[]*http.Request)error{return errors.New("redirect refused")};return &WestClient{HTTP:h,Now:time.Now}}
func gbk(value string)(string,error){v,_,e:=transform.String(simplifiedchinese.GBK.NewEncoder(),value);return v,e}
func signedForm(username,password string,at time.Time,values map[string]string)(url.Values,error){
 if strings.TrimSpace(username)==""||password==""{return nil,ErrCredentials}
 stamp:=strconv.FormatInt(at.UnixMilli(),10)
 raw,e:=gbk(username+password+stamp);if e!=nil{return nil,errors.New("接口账号字符编码无效")}
 sum:=md5.Sum([]byte(raw));fields:=url.Values{}
 for k,v:=range values{if k=="username"||k=="time"||k=="token"{return nil,errors.New("认证字段不可覆盖")};encoded,e:=gbk(v);if e!=nil{return nil,errors.New("填写内容包含不支持的字符")};fields.Set(k,encoded)}
 encoded,e:=gbk(username);if e!=nil{return nil,ErrCredentials};fields.Set("username",encoded);fields.Set("time",stamp);fields.Set("token",hex.EncodeToString(sum[:]));return fields,nil
}
func (c *WestClient) Call(ctx context.Context,cfg Settings,op OperationSpec,input map[string]string)(*ProviderResult,error){
 if cfg.Username==""||cfg.Password==""{return nil,ErrCredentials}
 if !strings.HasPrefix(op.Path,"/")||strings.Contains(op.Path,"..")||strings.HasPrefix(op.Path,"//"){return nil,errors.New("接口地址无效")}
 fields,e:=signedForm(cfg.Username,cfg.Password,c.Now(),input);if e!=nil{return nil,e}
 target:="https://api.west.cn/api/v2"+op.Path
 var body io.Reader
 if op.Method==http.MethodGet{sep:="?";if strings.Contains(target,"?"){sep="&"};target+=sep+fields.Encode()}else{body=strings.NewReader(fields.Encode())}
 req,e:=http.NewRequestWithContext(ctx,op.Method,target,body);if e!=nil{return nil,ErrConnection};req.Header.Set("Content-Type","application/x-www-form-urlencoded; charset=gb2312")
 return c.do(req,false)
}
func (c *WestClient) Upload(ctx context.Context,token,image string)(*ProviderResult,error){
 payload,_:=json.Marshal(map[string]string{"token":token,"file_org":image})
 req,e:=http.NewRequestWithContext(ctx,http.MethodPost,"https://netservice.vhostgo.com/wcfservice/Service1.svc/Wcf_AuditUploadFile",bytes.NewReader(payload));if e!=nil{return nil,ErrConnection};req.Header.Set("Content-Type","application/json")
 return c.do(req,true)
}
func (c *WestClient) do(req *http.Request,upload bool)(*ProviderResult,error){
 response,e:=c.HTTP.Do(req);if e!=nil{return nil,ErrConnection};defer response.Body.Close()
 raw,e:=io.ReadAll(io.LimitReader(response.Body,2<<20+1));if e!=nil||len(raw)>2<<20||response.StatusCode!=200{return nil,ErrConnection}
 if !utf8.Valid(raw){raw,_,e=transform.Bytes(simplifiedchinese.GBK.NewDecoder(),raw);if e!=nil{return nil,ErrConnection}}
 if upload{var result struct{D struct{Result int `json:"Result"`;Msg string `json:"Msg"`} `json:"d"`};if json.Unmarshal(raw,&result)!=nil||result.D.Result==0{return nil,ErrConnection};if result.D.Result!=200{return nil,&ProviderFailure{Code:strconv.Itoa(result.D.Result)}};return &ProviderResult{Result:"200",Data:json.RawMessage(`{}`)},nil}
 // The documented service returns both numeric and quoted status codes.
 var wire map[string]json.RawMessage;if json.Unmarshal(raw,&wire)!=nil{return nil,ErrConnection}
 var r ProviderResult
 for k,v:=range wire{switch k{case "result":r.Result=json.Number(strings.Trim(string(v),`"`));case "errcode":r.ErrCode=json.Number(strings.Trim(string(v),`"`));case "clientid":_ = json.Unmarshal(v,&r.ClientID);case "data":r.Data=v}}
 if number,e:=strconv.Atoi(string(r.Result));e!=nil||number<100||number>599{return nil,ErrConnection};if r.Result!="200"{code:=string(r.ErrCode);if _,e:=strconv.Atoi(code);e!=nil{code=string(r.Result)};if len(code)>12{code="500"};return nil,&ProviderFailure{Code:code}}
 if len(r.Data)==0{r.Data=json.RawMessage(`{}`)}
 // Never pass through provider free-text messages or authenticated download URLs.
 r.Msg="";return &r,nil
}
