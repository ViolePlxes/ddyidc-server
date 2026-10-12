package certification

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"ddyidc/internal/shared/snowflake"
	crypt "ddyidc/pkg/crypto"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const table = "customer.customer_realname"

var ErrUsed = errors.New("每人仅有一次免费实名认证机会")
var ErrRequired = errors.New("请先完成实名认证后再购买")

type Settings struct {
	Channel               string `json:"channel"`
	BizCode               string `json:"bizCode"`
	FreeAttempts          int    `json:"freeAttempts"`
	Fee                   string `json:"fee"`
	Enabled               bool   `json:"enabled"`
	RequirePurchase       bool   `json:"requirePurchase"`
	CompanyEnabled        bool   `json:"companyEnabled"`
	CompanyRequiresPerson bool   `json:"companyRequiresPerson"`
}
type Record struct {
	CertifyChannel    string     `json:"certifyChannel"`
	CertifyID         string     `json:"-"`
	CertifyConfig     string     `json:"-"`
	CertifyAttempts   int        `json:"certifyAttempts"`
	CertifyFeeOrder   int64      `json:"-"`
	ID                int64      `json:"id,string" gorm:"primaryKey;autoIncrement:false"`
	CustomerID        int64      `json:"customerId,string"`
	Type              int        `json:"type"`
	Name              string     `json:"name"`
	Phone             string     `json:"phone"`
	DocumentType      int        `json:"documentType"`
	IDCardNo          string     `json:"documentNumber"`
	DocumentHash      string     `json:"-"`
	IDCardFront       string     `json:"front"`
	IDCardBack        string     `json:"back"`
	CompanyName       string     `json:"companyName"`
	CompanyLicense    string     `json:"companyCode"`
	CompanyHash       string     `json:"-"`
	CompanyLicenseImg string     `json:"license"`
	LegalType         int        `json:"legalType"`
	Authorization     string     `json:"authorization"`
	Status            int        `json:"status"`
	RejectReason      string     `json:"rejectReason"`
	AuditorID         *int64     `json:"auditorId,string"`
	AuditedAt         *time.Time `json:"auditedAt"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

func (Record) TableName() string { return table }

type Input struct {
	Name           string `json:"name"`
	Phone          string `json:"phone"`
	DocumentType   int    `json:"documentType"`
	DocumentNumber string `json:"documentNumber"`
	Front          string `json:"front"`
	Back           string `json:"back"`
	CompanyName    string `json:"companyName"`
	CompanyCode    string `json:"companyCode"`
	License        string `json:"license"`
	LegalType      int    `json:"legalType"`
	Authorization  string `json:"authorization"`
}
type Service struct {
	alipayTransport http.RoundTripper // Test-only injection; no runtime configuration.
	db              *gorm.DB
	redis           redis.UniversalClient
	key             []byte
	root            string
}

func New(db *gorm.DB, r redis.UniversalClient, key []byte, root string) *Service {
	return &Service{db: db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}), redis: r, key: key, root: root}
}
func LoadSettings(db *gorm.DB) (Settings, error) {
	var row struct{ Value string }
	err := db.Table("system.system_configs").Select("value").Where("`key`=?", "certification.settings").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, err
	}
	out := Settings{Channel: "manual", BizCode: "FACE", FreeAttempts: 1, Fee: "0"}
	err = json.Unmarshal([]byte(row.Value), &out)
	return out, err
}
func Approved(db *gorm.DB, id int64) (bool, error) {
	var count int64
	err := db.Table(table).Where("customer_id=? AND status=2", id).Count(&count).Error
	return count == 1, err
}
func RequireApproved(db *gorm.DB, id int64) error {
	ok, err := Approved(db, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRequired
	}
	return nil
}
func CheckPurchase(db *gorm.DB, id int64) error {
	cfg, err := LoadSettings(db)
	if err != nil {
		return err
	}
	if cfg.Enabled && cfg.RequirePurchase {
		return RequireApproved(db, id)
	}
	return nil
}
func (s *Service) hash(value string) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(strings.ToUpper(strings.TrimSpace(value))))
	return hex.EncodeToString(h.Sum(nil))
}
func mask(value string, left, right int) string {
	r := []rune(value)
	if len(r) <= left+right {
		return strings.Repeat("*", len(r))
	}
	return string(r[:left]) + strings.Repeat("*", len(r)-left-right) + string(r[len(r)-right:])
}
func (s *Service) view(row Record, full bool) (Record, error) {
	var err error
	row.IDCardNo, err = crypt.Decrypt(row.IDCardNo, s.key)
	if err != nil {
		return Record{}, errors.New("认证资料读取失败")
	}
	row.CompanyLicense, err = crypt.Decrypt(row.CompanyLicense, s.key)
	if err != nil {
		return Record{}, errors.New("认证资料读取失败")
	}
	if !full {
		row.Name = mask(row.Name, 1, 0)
		row.CompanyName = mask(row.CompanyName, 1, 1)
		row.IDCardNo = mask(row.IDCardNo, 3, 4)
		row.CompanyLicense = mask(row.CompanyLicense, 3, 4)
		row.Phone = mask(row.Phone, 3, 4)
		row.IDCardFront = ""
		row.IDCardBack = ""
		row.CompanyLicenseImg = ""
		row.Authorization = ""
	}
	return row, nil
}

var namePattern = regexp.MustCompile(`^[\p{L}\p{M} ·.'-]+$`)
var phonePattern = regexp.MustCompile(`^\+?[1-9][0-9]{6,14}$`)
var mainlandID = regexp.MustCompile(`^[1-9][0-9]{16}[0-9X]$`)
var permitID = regexp.MustCompile(`^[A-Z0-9]{8,12}$`)
var passportID = regexp.MustCompile(`^[A-Z0-9]{5,20}$`)
var otherID = regexp.MustCompile(`^[A-Z0-9][A-Z0-9 -]{4,31}$`)
var companyCode = regexp.MustCompile(`^[0-9A-HJ-NPQRTUWXY]{18}$`)

func cleanText(s string, max int) bool {
	if utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || strings.ContainsRune("<>\\", r) {
			return false
		}
	}
	return true
}
func Validate(in *Input, kind int) error {
	in.Name = strings.TrimSpace(in.Name)
	in.Phone = strings.TrimSpace(in.Phone)
	in.DocumentNumber = strings.ToUpper(strings.TrimSpace(in.DocumentNumber))
	in.CompanyName = strings.TrimSpace(in.CompanyName)
	in.CompanyCode = strings.ToUpper(strings.TrimSpace(in.CompanyCode))
	if utf8.RuneCountInString(in.Name) < 2 || utf8.RuneCountInString(in.Name) > 64 || !namePattern.MatchString(in.Name) {
		return errors.New("姓名格式无效")
	}
	if !phonePattern.MatchString(in.Phone) {
		return errors.New("手机号格式无效")
	}
	if in.DocumentType < 1 || in.DocumentType > 8 {
		return errors.New("证件类型无效")
	}
	validDocument := false
	switch in.DocumentType {
	case 1, 4, 5:
		validDocument = mainlandID.MatchString(in.DocumentNumber)
	case 2, 3:
		validDocument = permitID.MatchString(in.DocumentNumber)
	case 6:
		validDocument = passportID.MatchString(in.DocumentNumber)
	case 7, 8:
		validDocument = otherID.MatchString(in.DocumentNumber)
	}
	if !validDocument {
		return errors.New("证件号码格式无效")
	}
	if kind == 1 && (in.Front == "" || in.Back == "") {
		return errors.New("请上传证件正反面")
	}
	if kind == 2 {
		if len(in.CompanyName) < 2 || !cleanText(in.CompanyName, 128) || !companyCode.MatchString(in.CompanyCode) {
			return errors.New("公司名称或统一社会信用代码格式无效")
		}
		if in.License == "" || in.LegalType < 1 || in.LegalType > 2 || in.LegalType == 2 && in.Authorization == "" {
			return errors.New("请提供营业执照及法人代表授权材料")
		}
	}
	return nil
}
func audit(tx *gorm.DB, actor int64, admin bool, action string, customer int64, detail map[string]any, ip string) error {
	kind := 2
	if admin {
		kind = 1
	}
	data, _ := json.Marshal(detail)
	return tx.Table("system.audit_logs").Create(map[string]any{"id": snowflake.GenID(), "operator_type": kind, "operator_id": actor, "action": "certification." + action, "target_type": "certification", "target_id": customer, "ip": ip, "detail": string(data), "created_at": time.Now(), "updated_at": time.Now()}).Error
}
func (s *Service) Submit(ctx context.Context, customer int64, kind int, in Input, ip string) (*Record, error) {
	var row Record
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner struct{ ID int64 }
		if err := tx.Table("customer.customers").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND deleted_at IS NULL AND status=1", customer).Take(&owner).Error; err != nil {
			return errors.New("客户不存在或已停用")
		}
		var count int64
		if err := tx.Model(&Record{}).Where("customer_id=?", customer).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrUsed
		}
		cfg, err := LoadSettings(tx)
		if err != nil {
			return err
		}
		if !cfg.Enabled {
			return errors.New("实名认证暂未开放")
		}
		if kind == 1 && cfg.Channel == "alipay" {
			return errors.New("请使用支付宝认证通道")
		}
		if kind == 2 && (!cfg.CompanyEnabled || cfg.CompanyRequiresPerson) {
			return errors.New("当前设置不允许提交企业认证；每人仅有一次免费机会")
		}
		if err := Validate(&in, kind); err != nil {
			return err
		}
		files := []string{in.Front, in.Back}
		if kind == 2 {
			files = []string{in.License}
			if in.LegalType == 2 {
				files = append(files, in.Authorization)
			}
		}
		for _, id := range files {
			if _, err := s.readUpload(id, customer, false); err != nil {
				return errors.New("证件照无效或不属于当前客户")
			}
		}
		encrypted, err := crypt.Encrypt(in.DocumentNumber, s.key)
		if err != nil {
			return err
		}
		company, err := crypt.Encrypt(in.CompanyCode, s.key)
		if err != nil {
			return err
		}
		row = Record{ID: snowflake.GenID(), CustomerID: customer, Type: kind, Name: in.Name, Phone: in.Phone, DocumentType: in.DocumentType, IDCardNo: encrypted, DocumentHash: s.hash(in.DocumentNumber), Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if kind == 1 {
			row.IDCardFront = in.Front
			row.IDCardBack = in.Back
		} else {
			row.CompanyName = in.CompanyName
			row.CompanyLicense = company
			row.CompanyHash = s.hash(in.CompanyCode)
			row.CompanyLicenseImg = in.License
			row.LegalType = in.LegalType
			if in.LegalType == 2 {
				row.Authorization = in.Authorization
			}
		}
		if err := tx.Create(&row).Error; err != nil {
			var existing int64
			tx.Model(&Record{}).Where("customer_id=?", customer).Count(&existing)
			if existing > 0 {
				return ErrUsed
			}
			return errors.New("认证提交失败")
		}
		return audit(tx, customer, false, "submit", customer, map[string]any{"recordId": row.ID, "type": kind, "status": 1}, ip)
	})
	if err != nil {
		return nil, err
	}
	out, err := s.view(row, false)
	return &out, err
}
func (s *Service) Review(ctx context.Context, id, actor int64, action, reason, ip string) error {
	reason = strings.TrimSpace(reason)
	if action != "pass" && action != "reject" && action != "unlock" {
		return errors.New("审核操作无效")
	}
	if !cleanText(reason, 255) {
		return errors.New("操作说明格式无效")
	}
	if (action == "reject" || action == "unlock") && (reason == "" || !cleanText(reason, 255)) {
		return errors.New("请填写有效原因（最多255字）")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Record
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, id).Error; err != nil {
			return errors.New("认证记录不存在")
		}
		if row.CertifyChannel == "alipay" && action == "unlock" {
			return errors.New("支付宝认证不支持解除限制")
		}
		if action == "unlock" {
			if row.Status != 3 {
				return errors.New("仅可解除已拒绝记录的限制")
			}
			if err := audit(tx, actor, true, "unlock", row.CustomerID, map[string]any{"recordId": row.ID, "reason": reason, "previousStatus": row.Status}, ip); err != nil {
				return err
			}
			return tx.Delete(&row).Error
		}
		if row.Status != 1 {
			return errors.New("该记录已经审核，不能重复审核")
		}
		status := 2
		if action == "reject" {
			status = 3
		}
		if err := tx.Model(&row).Updates(map[string]any{"status": status, "reject_reason": reason, "auditor_id": actor, "audited_at": time.Now(), "updated_at": time.Now()}).Error; err != nil {
			return err
		}
		return audit(tx, actor, true, action, row.CustomerID, map[string]any{"recordId": row.ID, "reason": reason, "status": status}, ip)
	})
}
