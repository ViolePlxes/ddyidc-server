package adminread

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"ddyidc/internal/shared/snowflake"
	"ddyidc/pkg/auth"
	"gorm.io/gorm"
)

const bankTransferKey = "wallet.bank_transfer"

type bankTransferConfig struct {
	Enabled      bool   `json:"enabled"`
	CompanyName  string `json:"companyName"`
	BankName     string `json:"bankName"`
	BankAccount  string `json:"bankAccount"`
	BankCode     string `json:"bankCode"`
	Instructions string `json:"instructions"`
}
type savedBankTransfer struct {
	bankTransferConfig
	UpdatedBy  int64  `json:"updatedBy"`
	UpdatedAt  string `json:"updatedAt"`
	OperatorIP string `json:"operatorIp"`
}

var bankAccountPattern = regexp.MustCompile(`^[0-9]{8,40}$`)
var bankCodePattern = regexp.MustCompile(`^[0-9]{12}$`)

func normalizeBankTransfer(c *bankTransferConfig) error {
	c.CompanyName = strings.TrimSpace(c.CompanyName)
	c.BankName = strings.TrimSpace(c.BankName)
	c.BankAccount = strings.ReplaceAll(strings.TrimSpace(c.BankAccount), " ", "")
	c.BankCode = strings.TrimSpace(c.BankCode)
	c.Instructions = strings.TrimSpace(c.Instructions)
	if utf8.RuneCountInString(c.CompanyName) > 200 || utf8.RuneCountInString(c.BankName) > 200 || utf8.RuneCountInString(c.Instructions) > 4000 {
		return errors.New("收款信息或说明过长")
	}
	if c.Enabled && (c.CompanyName == "" || c.BankName == "" || c.BankAccount == "") {
		return errors.New("启用对公汇款前请填写开户名称、开户银行和银行账号")
	}
	if c.BankAccount != "" && !bankAccountPattern.MatchString(c.BankAccount) {
		return errors.New("银行账号应为 8 至 40 位数字")
	}
	if c.BankCode != "" && !bankCodePattern.MatchString(c.BankCode) {
		return errors.New("联行号应为 12 位数字")
	}
	for _, v := range []string{c.CompanyName, c.BankName} {
		if strings.ContainsAny(v, "\r\n\x00") {
			return errors.New("开户信息不能包含换行或空字符")
		}
	}
	return nil
}
func (h *handler) loadBankTransfer(r *http.Request) (bankTransferConfig, error) {
	var row struct{ Value string }
	err := h.db.WithContext(r.Context()).Table("system.system_configs").Select("value").Where("`key`=?", bankTransferKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return bankTransferConfig{}, nil
	}
	if err != nil {
		return bankTransferConfig{}, err
	}
	var config bankTransferConfig
	if err = json.Unmarshal([]byte(row.Value), &config); err != nil {
		return config, err
	}
	return config, normalizeBankTransfer(&config)
}
func publicBankTransfer(c bankTransferConfig) bankTransferConfig {
	if !c.Enabled {
		return bankTransferConfig{}
	}
	return c
}
func (h *handler) customerBankTransfer(w http.ResponseWriter, r *http.Request) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		writeError(w, 401, "请先登录")
		return
	}
	claims, err := h.auth.ParseAccessTokenFor(r.Context(), auth.SessionUser, strings.TrimPrefix(header, "Bearer "))
	if err != nil || claims.Role != "customer" {
		writeError(w, 401, "用户会话无效")
		return
	}
	c, err := h.loadBankTransfer(r)
	if err != nil {
		writeError(w, 500, "收款信息读取失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, publicBankTransfer(c))
}
func (h *handler) adminBankTransfer(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	actor, _, err := h.balanceActor(r)
	if err != nil {
		writeError(w, 403, "管理员不可用")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		c, err := h.loadBankTransfer(r)
		if err != nil {
			writeError(w, 500, "收款信息读取失败")
			return
		}
		writeJSON(w, c)
		return
	}
	var c bankTransferConfig
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		writeError(w, 400, "收款信息格式错误")
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeError(w, 400, "收款信息格式错误")
		return
	}
	if err := normalizeBankTransfer(&c); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	value, err := json.Marshal(savedBankTransfer{bankTransferConfig: c, UpdatedBy: actor, UpdatedAt: time.Now().UTC().Format(time.RFC3339), OperatorIP: requestIP(r)})
	if err != nil {
		writeError(w, 500, "收款信息保存失败")
		return
	}
	err = h.db.WithContext(r.Context()).Exec("INSERT INTO system.system_configs(id,`key`,value,description) VALUES(?,?,?,'对公汇款收款信息') ON DUPLICATE KEY UPDATE value=VALUES(value)", snowflake.GenID(), bankTransferKey, string(value)).Error
	if err != nil {
		writeError(w, 500, "收款信息保存失败")
		return
	}
	writeJSON(w, c)
}
