package marketing

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	dom "ddyidc/internal/marketing/domain"
	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var codeFormat = regexp.MustCompile(`^[A-Z0-9]{4,12}$`)

type CodePolicy struct {
	BlockedTerms  []string `json:"blockedTerms"`
	ReservedCodes []string `json:"reservedCodes"`
	Version       int64    `json:"version"`
}

var builtinTerms = []string{"FANDONG", "FENLIE", "TAIDU", "ZANGDU", "KONGBU", "TERROR", "NAZI", "SHA REN", "ZHADAN", "FANDU", "MAIDU", "ZHAPIAN", "XIEDU"}
var builtinReserved = []string{"ADMIN", "ROOT", "OFFICIAL", "SUPPORT", "DDYIDC", "VIP", "5201314", "1314520", "1688"}

func normalizedCode(v string) string { return strings.ToUpper(strings.TrimSpace(v)) }
func codeVariants(v string) []string {
	v = normalizedCode(v)
	// Digits used as letters and repeated-letter obfuscation are checked too.
	r := strings.NewReplacer("0", "O", "1", "I", "3", "E", "4", "A", "5", "S", "7", "T", "8", "B").Replace(v)
	collapse := func(s string) string {
		var out strings.Builder
		var last rune
		for _, c := range s {
			if c != last {
				out.WriteRune(c)
			}
			last = c
		}
		return out.String()
	}
	return []string{v, r, collapse(v), collapse(r)}
}
func codeIssue(code string, p CodePolicy) string {
	code = normalizedCode(code)
	if !codeFormat.MatchString(code) {
		return "格式不符：仅支持4～12位英文字母和数字"
	}
	for _, term := range append(append([]string{}, builtinTerms...), p.BlockedTerms...) {
		term = strings.ReplaceAll(normalizedCode(term), " ", "")
		if term == "" {
			continue
		}
		for _, v := range codeVariants(code) {
			if strings.Contains(v, term) {
				return "内容不符合平台规则，请更换优惠码"
			}
		}
	}
	for _, v := range append(append([]string{}, builtinReserved...), p.ReservedCodes...) {
		if code == normalizedCode(v) {
			return "该优惠码由平台保留"
		}
	}
	for _, v := range []string{"ADMIN", "OFFICIAL", "SUPPORT", "DDYIDC"} {
		if strings.Contains(code, v) {
			return "该优惠码由平台保留"
		}
	}
	numeric := true
	for _, c := range code {
		if c < '0' || c > '9' {
			numeric = false
		}
	}
	sameAll, upAll, downAll := true, true, true
	for i := 1; i < len(code); i++ {
		sameAll = sameAll && code[i] == code[0]
		upAll = upAll && code[i] == code[i-1]+1
		downAll = downAll && code[i]+1 == code[i-1]
	}
	if sameAll || upAll || downAll {
		return "该优惠码由平台保留"
	}
	for n := 1; n <= len(code)/2; n++ {
		if len(code)%n == 0 && strings.Repeat(code[:n], len(code)/n) == code {
			return "该优惠码由平台保留"
		}
	}
	if numeric {
		distinct := map[byte]bool{}
		palindrome := true
		for i := range code {
			distinct[code[i]] = true
			palindrome = palindrome && code[i] == code[len(code)-1-i]
		}
		if len(distinct) <= 2 || palindrome {
			return "该优惠码由平台保留"
		}
		same, up, down := true, true, true
		for i := 1; i < len(code); i++ {
			same = same && code[i] == code[0]
			up = up && int(code[i]-'0') == (int(code[i-1]-'0')+1)%10
			down = down && int(code[i]-'0') == (int(code[i-1]-'0')+9)%10
		}
		if same || up || down {
			return "该优惠码由平台保留"
		}
		for n := 1; n <= len(code)/2; n++ {
			if len(code)%n == 0 && strings.Repeat(code[:n], len(code)/n) == code {
				return "该优惠码由平台保留"
			}
		}
		for i := 2; i < len(code); i++ {
			if code[i] == code[i-1] && code[i] == code[i-2] {
				return "该优惠码由平台保留"
			}
		}
	}
	return ""
}

// LockCodePolicy is the common lock order: policy, then customer code rows.
func LockCodePolicy(ctx context.Context, tx *gorm.DB) error {
	var row struct{ ID int }
	return tx.WithContext(ctx).Table("marketing.code_policy").Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Take(&row, "id=1").Error
}
func policyFrom(tx *gorm.DB) (CodePolicy, error) {
	var row struct {
		Payload string
		Version int64
	}
	e := tx.Table("marketing.code_policy").Where("id=1").Take(&row).Error
	var p CodePolicy
	if e == nil {
		e = json.Unmarshal([]byte(row.Payload), &p)
		p.Version = row.Version
	}
	return p, e
}
func (s *Service) CodePolicy(ctx context.Context) (CodePolicy, error) {
	return policyFrom(s.db.WithContext(ctx))
}
func (s *Service) SaveCodePolicy(ctx context.Context, p CodePolicy, admin int64, ip, reason string) error {
	if admin <= 0 || ip == "" || len(strings.TrimSpace(reason)) < 2 || len([]rune(reason)) > 250 || len(p.BlockedTerms) > 500 || len(p.ReservedCodes) > 500 {
		return errors.New("规则或审计参数无效")
	}
	for _, list := range [][]string{p.BlockedTerms, p.ReservedCodes} {
		for i, v := range list {
			v = normalizedCode(v)
			if !regexp.MustCompile(`^[A-Z0-9]{2,32}$`).MatchString(v) {
				return errors.New("规则项须为2～32位英文字母或数字")
			}
			list[i] = v
		}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := LockCodePolicy(ctx, tx); e != nil {
			return e
		}
		old, e := policyFrom(tx)
		if e != nil {
			return e
		}
		if p.Version != old.Version {
			return errors.New("规则已更新，请刷新后重试")
		}
		p.Version = old.Version + 1
		raw, _ := json.Marshal(p)
		if e = tx.Table("marketing.code_policy").Where("id=1").Updates(map[string]any{"payload": string(raw), "version": p.Version, "scan_cursor": 0, "updated_at": time.Now().UTC()}).Error; e != nil {
			return e
		}
		return codeAudit(tx, admin, ip, "policy", 0, "", "", reason)
	})
}
func codeAudit(tx *gorm.DB, admin int64, ip, action string, user int64, old, new, reason string) error {
	return tx.Table("marketing.code_audits").Create(map[string]any{"id": snowflake.GenID(), "admin_id": admin, "operator_ip": ip, "action": action, "user_id": user, "old_code": old, "new_code": new, "notes": reason, "created_at": time.Now().UTC()}).Error
}
func claimCode(tx *gorm.DB, code string, user int64, kind string) error {
	var n int64
	if e := tx.Table("marketing.code_claims").Where("code=?", code).Count(&n).Error; e != nil {
		return e
	}
	if n > 0 {
		return errors.New("该优惠码已使用或已封存，请更换")
	}
	return tx.Table("marketing.code_claims").Create(map[string]any{"code": code, "owner_id": user, "kind": kind, "created_at": time.Now().UTC()}).Error
}
func randomCode(tx *gorm.DB, p CodePolicy, user int64) (string, error) {
	var configured struct{ Value string }
	if e := tx.Table("system.system_configs").Select("value").Where("`key`=?", CfgInviteCodeLength).Scan(&configured).Error; e != nil {
		return "", e
	}
	length, e := strconv.Atoi(configured.Value)
	if e != nil || length < 4 || length > 12 {
		length = 8
	}
	for i := 0; i < 50; i++ {
		v := randFromCharset(length, "ABCDEFGHJKLMNPQRSTUVWXYZ23456789")
		if codeIssue(v, p) != "" {
			continue
		}
		var n int64
		if e := tx.Table("marketing.invites").Where("code=? OR pending_code=?", v, v).Count(&n).Error; e != nil {
			return "", e
		}
		if n != 0 {
			continue
		}
		if e := claimCode(tx, v, user, "issued"); e != nil {
			if strings.Contains(e.Error(), "已使用") {
				continue
			}
			return "", e
		}
		return v, nil
	}
	return "", errors.New("优惠码暂时无法分配，请稍后重试")
}
func retireCode(tx *gorm.DB, code string, user int64) error {
	// INSERT IGNORE also seals legacy codes not yet present in the claim ledger.
	if e := tx.Exec("INSERT IGNORE INTO marketing.code_claims(code,owner_id,kind,created_at) VALUES(?,?,'retired',?)", code, user, time.Now().UTC()).Error; e != nil {
		return e
	}
	return tx.Table("marketing.code_claims").Where("code=? AND owner_id=?", code, user).Update("kind", "retired").Error
}
func (s *Service) applyGovernedCode(ctx context.Context, user int64, raw string) (*InviteDTO, error) {
	var result *dom.Invite
	e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := LockCodePolicy(ctx, tx); e != nil {
			return e
		}
		p, e := policyFrom(tx)
		if e != nil {
			return e
		}
		code := normalizedCode(raw)
		if raw != "" && code == "" {
			return errors.New("优惠码不能为空白")
		}
		if code != "" {
			if issue := codeIssue(code, p); issue != "" {
				return errors.New(issue)
			}
		}
		var v dom.Invite
		created := false
		e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", user).Take(&v).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			v = dom.Invite{UserID: user, Status: dom.InviteActive}
			v.Code, e = randomCode(tx, p, user)
			if e != nil {
				return e
			}
			if e = tx.Create(&v).Error; e != nil {
				return e
			}
			created = true
		} else if e != nil {
			return e
		}
		if v.PendingCode != nil {
			return errors.New("已有自定义优惠码待审核")
		}
		if code == "" {
			if created {
				result = &v
				return nil
			}
			if v.Status == dom.InviteActive {
				return errors.New("已有生效的优惠码")
			}
			old := v.Code
			if e = retireCode(tx, old, user); e != nil {
				return e
			}
			v.Code, e = randomCode(tx, p, user)
			if e != nil {
				return e
			}
			v.Status = dom.InviteActive
			v.RejectReason = nil
			if e = codeAudit(tx, 0, "system", "replace", user, old, v.Code, "重新分配系统优惠码"); e != nil {
				return e
			}
		} else {
			if e = claimCode(tx, code, user, "pending"); e != nil {
				return e
			}
			v.PendingCode = &code
			v.RejectReason = nil
		}
		if e = tx.Save(&v).Error; e != nil {
			return e
		}
		result = &v
		return nil
	})
	if e != nil {
		return nil, e
	}
	return inviteToDTO(result), nil
}

// ensureSystemCode lazily allocates a safe code and rotates invalid legacy codes.
func (s *Service) ensureSystemCode(ctx context.Context, user int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := LockCodePolicy(ctx, tx); e != nil {
			return e
		}
		p, e := policyFrom(tx)
		if e != nil {
			return e
		}
		var v dom.Invite
		e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", user).Take(&v).Error
		if errors.Is(e, gorm.ErrRecordNotFound) {
			v = dom.Invite{UserID: user, Status: dom.InviteActive}
			v.Code, e = randomCode(tx, p, user)
			if e != nil {
				return e
			}
			return tx.Create(&v).Error
		}
		if e != nil {
			return e
		}
		return reclaimCode(tx, p, &v, 0, "system", "平台规则自动核对")
	})
}
func reclaimCode(tx *gorm.DB, p CodePolicy, v *dom.Invite, admin int64, ip, reason string) error {
	issue := codeIssue(v.Code, p)
	var claim struct {
		OwnerID int64
		Kind    string
	}
	claimErr := tx.Table("marketing.code_claims").Where("code=?", v.Code).Take(&claim).Error
	if errors.Is(claimErr, gorm.ErrRecordNotFound) {
		if e := claimCode(tx, v.Code, v.UserID, "issued"); e != nil {
			return e
		}
	} else if claimErr != nil {
		return claimErr
	} else if claim.OwnerID != v.UserID || claim.Kind == "retired" {
		issue = "原优惠码已封存或归属异常"
	}
	pendingBad := v.PendingCode != nil && codeIssue(*v.PendingCode, p) != ""
	if issue == "" && !pendingBad {
		if v.Status != dom.InvitePending || v.PendingCode != nil {
			return nil
		}
		old := v.Code
		v.PendingCode = &old
		code, e := randomCode(tx, p, v.UserID)
		if e != nil {
			return e
		}
		v.Code = code
		v.Status = dom.InviteActive
		if e = tx.Save(v).Error; e != nil {
			return e
		}
		return codeAudit(tx, admin, ip, "default", v.UserID, old, code, "保留原申请并分配系统优惠码")
	}
	if pendingBad {
		if e := retireCode(tx, *v.PendingCode, v.UserID); e != nil {
			return e
		}
		v.PendingCode = nil
		v.RejectReason = dom.StrPtr("申请不符合最新优惠码规则")
	}
	old := v.Code
	if issue != "" {
		if e := retireCode(tx, old, v.UserID); e != nil {
			return e
		}
		code, e := randomCode(tx, p, v.UserID)
		if e != nil {
			return e
		}
		v.Code = code
		if v.Status == dom.InvitePending {
			v.Status = dom.InviteActive
		}
		v.RejectReason = dom.StrPtr("原优惠码已回收，历史客户归属和收益不变")
	}
	if e := tx.Save(v).Error; e != nil {
		return e
	}
	return codeAudit(tx, admin, ip, "reclaim", v.UserID, old, v.Code, reason+"；"+issue)
}

type CodeCandidate struct {
	ID          int64   `json:"id,string"`
	UserID      int64   `json:"userId,string"`
	Code        string  `json:"code"`
	PendingCode *string `json:"pendingCode"`
	Reason      string  `json:"reason"`
}

func (s *Service) CodeCandidates(ctx context.Context) ([]CodeCandidate, error) {
	p, e := s.CodePolicy(ctx)
	if e != nil {
		return nil, e
	}
	var rows []dom.Invite
	if e = s.db.WithContext(ctx).Order("id").Limit(5000).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := []CodeCandidate{}
	for _, v := range rows {
		issue := codeIssue(v.Code, p)
		if issue == "" && v.PendingCode != nil {
			issue = codeIssue(*v.PendingCode, p)
		}
		if issue != "" {
			out = append(out, CodeCandidate{v.ID, v.UserID, v.Code, v.PendingCode, issue})
		}
	}
	return out, nil
}
func (s *Service) ReclaimCode(ctx context.Context, id, admin int64, ip, reason string) error {
	if id <= 0 || admin <= 0 || ip == "" || len(strings.TrimSpace(reason)) < 2 || len([]rune(reason)) > 250 {
		return errors.New("回收审计参数无效")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := LockCodePolicy(ctx, tx); e != nil {
			return e
		}
		p, e := policyFrom(tx)
		if e != nil {
			return e
		}
		var v dom.Invite
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&v, "id=?", id).Error; e != nil {
			return e
		}
		return reclaimCode(tx, p, &v, admin, ip, reason)
	})
}
func (s *Service) MaintainCodes(ctx context.Context) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := LockCodePolicy(ctx, tx); e != nil {
			return e
		}
		p, e := policyFrom(tx)
		if e != nil {
			return e
		}
		var state struct{ ScanCursor int64 }
		if e = tx.Table("marketing.code_policy").Where("id=1").Take(&state).Error; e != nil {
			return e
		}
		var rows []dom.Invite
		if e = tx.Where("id>?", state.ScanCursor).Order("id").Limit(100).Find(&rows).Error; e != nil {
			return e
		}
		cursor := int64(0)
		for i := range rows {
			if e = reclaimCode(tx, p, &rows[i], 0, "system", "规则巡检自动回收"); e != nil {
				return e
			}
			cursor = rows[i].ID
		}
		return tx.Table("marketing.code_policy").Where("id=1").Update("scan_cursor", cursor).Error
	})
}
func (s *Service) StartCodeGovernance(ctx context.Context) {
	go func() {
		run := func() { distsync.Task(ctx, "marketing_code_governance", time.Minute, s.MaintainCodes) }
		run()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}
func (s *Service) IsSuperAdmin(ctx context.Context, id int64) bool {
	var n int64
	return s.db.WithContext(ctx).Table("system.admins").Where("id=? AND status=1 AND is_super=1", id).Count(&n).Error == nil && n == 1
}
func (s *Service) CodeAudits(ctx context.Context) ([]map[string]any, error) {
	var out []map[string]any
	e := s.db.WithContext(ctx).Table("marketing.code_audits").Select("CAST(id AS CHAR) id,CAST(admin_id AS CHAR) admin_id,action,CAST(user_id AS CHAR) user_id,old_code,new_code,notes,created_at").Order("created_at DESC,id DESC").Limit(100).Find(&out).Error
	return out, e
}
func (s *Service) GovernCode(ctx context.Context, id, admin int64, ip, action, reason, expectedCode string) error {
	if admin <= 0 || ip == "" || len(strings.TrimSpace(reason)) < 2 || len([]rune(reason)) > 250 {
		return errors.New("请填写至少2字的操作说明")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := LockCodePolicy(ctx, tx); e != nil {
			return e
		}
		var v dom.Invite
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&v, "id=?", id).Error; e != nil {
			return e
		}
		candidate := v.Code
		if (action == "approve" || action == "reject") && v.PendingCode != nil {
			candidate = *v.PendingCode
		}
		if expectedCode == "" || candidate != expectedCode {
			return errors.New("优惠码记录已变化，请刷新后重试")
		}
		service := NewService(tx, nil)
		var e error
		switch action {
		case "approve", "reject":
			e = service.AdminReviewInvite(ctx, id, action == "approve", reason)
		case "enable":
			e = service.AdminSetInviteStatus(ctx, id, dom.InviteActive)
		case "disable":
			e = service.AdminSetInviteStatus(ctx, id, dom.InviteDisabled)
		case "reclaim":
			p, err := policyFrom(tx)
			if err != nil {
				return err
			}
			return reclaimCode(tx, p, &v, admin, ip, reason)
		default:
			return errors.New("操作无效")
		}
		if e != nil {
			return e
		}
		var after dom.Invite
		if e = tx.Take(&after, "id=?", id).Error; e != nil {
			return e
		}
		oldCode := v.Code
		if action == "reject" && v.PendingCode != nil {
			oldCode = *v.PendingCode
		}
		return codeAudit(tx, admin, ip, action, v.UserID, oldCode, after.Code, reason)
	})
}

func codeClaimUsable(tx *gorm.DB, v *dom.Invite) error {
	var n int64
	if e := tx.Table("marketing.code_claims").Where("code=? AND owner_id=? AND kind<>'retired'", v.Code, v.UserID).Count(&n).Error; e != nil {
		return e
	}
	if n != 1 {
		return errors.New("优惠码无效或已调整，请使用新的优惠链接")
	}
	return nil
}
