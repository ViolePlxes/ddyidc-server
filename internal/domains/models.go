package domains

import (
 "context"
 "database/sql"
 _ "embed"
 "encoding/json"
 "errors"
 "fmt"
 "strings"
 "time"

 balance "ddyidc/internal/billing/balance"
 "ddyidc/internal/shared/snowflake"
 "ddyidc/pkg/crypto"
 "gorm.io/gorm"
 "gorm.io/gorm/clause"
)

//go:embed schema.sql
var schemaSQL string
type Settings struct{Enabled bool `json:"enabled"`;Username string `json:"username"`;Password string `json:"password,omitempty" gorm:"-"`;PasswordCipher string `json:"-"`;PasswordSet bool `json:"passwordSet" gorm:"-"`;MarkupBPS int `json:"markupBps"`;ServiceFee balance.Money `json:"serviceFee"`;RedemptionCost balance.Money `json:"redemptionCost"`;Revision int64 `json:"revision"`;Notes string `json:"notes,omitempty" gorm:"-"`}
type Asset struct{ID int64 `json:"id,string"`;CustomerID int64 `json:"-"`;Domain string `json:"domain"`;Status string `json:"status"`;ExpiresAt *time.Time `json:"expiresAt"`;CreatedAt time.Time `json:"createdAt"`;UpdatedAt time.Time `json:"updatedAt"`}
func(Asset)TableName()string{return "system.domain_assets"}
type Template struct{ID int64 `json:"id,string"`;CustomerID int64 `json:"-"`;ProviderID string `json:"-"`;Label string `json:"label"`;Status string `json:"status"`;CreatedAt time.Time `json:"createdAt"`;UpdatedAt time.Time `json:"updatedAt"`}
func(Template)TableName()string{return "system.domain_templates"}
type Operation struct{ID int64 `json:"id,string"`;CustomerID int64 `json:"-"`;Action string `json:"action"`;Domain string `json:"domain"`;RequestID string `json:"requestId"`;InputHash string `json:"-"`;Status string `json:"status"`;Result json.RawMessage `json:"result"`;ErrorCode string `json:"errorCode"`;CreatedAt time.Time `json:"createdAt"`;UpdatedAt time.Time `json:"updatedAt"`}
func(Operation)TableName()string{return "system.domain_operations"}
type Quote struct{ID int64 `json:"id,string"`;CustomerID int64 `json:"-"`;Action string `json:"action"`;Domain string `json:"domain"`;PayloadCipher string `json:"-"`;Cost balance.Money `json:"-"`;Amount balance.Money `json:"amount"`;ConfigRevision int64 `json:"-"`;ExpiresAt time.Time `json:"expiresAt"`;OrderID *int64 `json:"orderId,string,omitempty"`;CreatedAt time.Time `json:"createdAt"`}
func(Quote)TableName()string{return "system.domain_quotes"}
type DomainOrder struct{ID int64 `json:"id,string"`;CustomerID int64 `json:"-"`;QuoteID int64 `json:"-"`;Action string `json:"action"`;Domain string `json:"domain"`;PayloadCipher string `json:"-"`;Cost balance.Money `json:"-"`;Amount balance.Money `json:"amount"`;Status string `json:"status"`;ErrorCode string `json:"errorCode"`;CreatedAt time.Time `json:"createdAt"`;UpdatedAt time.Time `json:"updatedAt"`}
func(DomainOrder)TableName()string{return "system.domain_orders"}
type Service struct{db *gorm.DB;key []byte;provider *WestClient;now func()time.Time}
func New(database *gorm.DB,key []byte)*Service{return &Service{database,key,NewWestClient(),time.Now}}
var ErrOwnership=errors.New("域名或信息模板不存在，或无权操作")
var ErrPending=errors.New("请求已提交或结果待核对，请查看操作记录，勿重复提交")
func(s *Service) Settings(ctx context.Context,private bool)(Settings,error){var v Settings;e:=s.db.WithContext(ctx).Table("system.domain_settings").Where("id=1").Take(&v).Error;if e!=nil{return v,e};v.PasswordSet=v.PasswordCipher!="";if private{v.Password,e=crypto.Decrypt(v.PasswordCipher,s.key)};return v,e}
func(s *Service) ready(ctx context.Context)(Settings,error){cfg,e:=s.Settings(ctx,true);if e!=nil{return cfg,e};if !cfg.Enabled{return cfg,errors.New("域名服务已暂停")};if cfg.Username==""||cfg.Password==""{return cfg,ErrCredentials};return cfg,nil}
func(s *Service) SaveSettings(ctx context.Context,admin int64,ip string,v Settings)error{
 if admin<=0||len(strings.TrimSpace(v.Notes))<2||len(v.Notes)>500||len(v.Username)>128||len(v.Password)>1024||v.MarkupBPS<0||v.MarkupBPS>100000||v.ServiceFee<0||v.RedemptionCost<0{return errors.New("配置参数或操作说明无效")}
 return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB)error{
  var old Settings;if e:=tx.Table("system.domain_settings").Clauses(clause.Locking{Strength:"UPDATE"}).Where("id=1").Take(&old).Error;e!=nil{return e};if v.Revision!=old.Revision{return errors.New("配置已变化，请刷新后重试")}
  if old.Username!=""&&old.Username!=strings.TrimSpace(v.Username){for _,table:=range []string{"system.domain_assets","system.domain_templates","system.domain_orders"}{var count int64;if e:=tx.Table(table).Count(&count).Error;e!=nil{return e};if count>0{return errors.New("已有域名业务记录，不能直接更换接口账号")}}}
  if old.PasswordCipher!=""&&old.Username!=strings.TrimSpace(v.Username)&&v.Password==""{return errors.New("更换接口账号时请同时填写对应API密码")}
  sealed:=old.PasswordCipher;if v.Password!=""{var e error;sealed,e=crypto.Encrypt(v.Password,s.key);if e!=nil{return errors.New("凭据加密失败")}}
  if e:=tx.Table("system.domain_settings").Where("id=1").Updates(map[string]any{"enabled":v.Enabled,"username":strings.TrimSpace(v.Username),"password_cipher":sealed,"markup_bps":v.MarkupBPS,"service_fee":v.ServiceFee,"redemption_cost":v.RedemptionCost,"revision":old.Revision+1,"updated_at":s.now()}).Error;e!=nil{return e}
  return tx.Table("system.domain_admin_audits").Create(map[string]any{"id":snowflake.GenID(),"admin_id":admin,"operator_ip":ip,"notes":v.Notes,"action":"save_settings","created_at":s.now()}).Error
 })
}
func ApplyRuntimePrivileges(ctx context.Context,db *sql.DB,user,host string)error{
 if user!="ddyidc_runtime"||(host!="127.0.0.1"&&host!="::1"){return errors.New("unexpected domain runtime identity")}
 for _,table:=range []string{"domain_rate_limits","domain_settings","domain_assets","domain_templates","domain_operations","domain_quotes","domain_orders","domain_admin_audits"}{rights:="SELECT,INSERT,UPDATE";if table=="domain_admin_audits"{rights="SELECT,INSERT"};if _,e:=db.ExecContext(ctx,fmt.Sprintf("GRANT %s ON system.%s TO '%s'@'%s'",rights,table,user,host));e!=nil{return errors.New("domain runtime grants failed")}}
 return nil
}
func(s *Service) Assets(ctx context.Context,user int64)([]Asset,error){rows:=[]Asset{};e:=s.db.WithContext(ctx).Where("customer_id=?",user).Order("id DESC").Limit(1000).Find(&rows).Error;return rows,e}
func(s *Service) Templates(ctx context.Context,user int64)([]Template,error){rows:=[]Template{};e:=s.db.WithContext(ctx).Where("customer_id=? AND status<>'deleted'",user).Order("id DESC").Limit(500).Find(&rows).Error;return rows,e}
func(s *Service) Records(ctx context.Context,user int64)([]Operation,error){rows:=[]Operation{};e:=s.db.WithContext(ctx).Select("id,action,domain,request_id,status,error_code,created_at,updated_at").Where("customer_id=?",user).Order("created_at DESC,id DESC").Limit(100).Find(&rows).Error;return rows,e}
