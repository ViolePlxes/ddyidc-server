package cdnsell

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"ddyidc/internal/shared/snowflake"
	cryptoutil "ddyidc/pkg/crypto"
	"gorm.io/gorm"
)

var (
	ErrForbidden  = errors.New("无权访问该 CDN 资源")
	domainPattern = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

func normalizeDomain(value string) (string, error) {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if len(value) > 253 || !domainPattern.MatchString(value) {
		return "", errors.New("域名格式无效，仅支持已备案的普通域名")
	}
	return value, nil
}

func validateDomainInput(input *DomainInput) error {
	domain, err := normalizeDomain(input.Domain)
	if err != nil {
		return err
	}
	input.Domain = domain
	if input.BusinessType != "web" && input.BusinessType != "video" && input.BusinessType != "download" {
		return errors.New("业务类型无效")
	}
	if input.AccelerateRegion != "mainland" && input.AccelerateRegion != "overseas" && input.AccelerateRegion != "global" {
		return errors.New("加速区域无效")
	}
	if input.OriginType != "domain" && input.OriginType != "ip" {
		return errors.New("源站类型无效")
	}
	if input.OriginProtocol != "http" && input.OriginProtocol != "follow" && input.OriginProtocol != "https" {
		return errors.New("回源协议无效")
	}
	input.OriginHost = strings.TrimSpace(input.OriginHost)
	if input.OriginType == "domain" {
		origin, e := normalizeDomain(input.OriginHost)
		if e != nil {
			return errors.New("源站域名格式无效")
		}
		input.OriginHost = origin
		if origin == input.Domain {
			return errors.New("源站域名不能与加速域名相同")
		}
	} else if ip := net.ParseIP(input.OriginHost); ip == nil || !isPublicIP(ip) {
		return errors.New("源站 IP 必须是公网 IP")
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast()
}

func (s *Service) CreateDomain(ctx context.Context, userID int64, input DomainInput) (*DomainRecord, error) {
	if err := validateDomainInput(&input); err != nil {
		return nil, err
	}
	var accountID int64
	if err := s.db.WithContext(ctx).Table(accountsTable).Select("id").Where("status=1").Order("sort,id").Limit(1).Scan(&accountID).Error; err != nil {
		return nil, err
	}
	if accountID == 0 {
		return nil, errors.New("暂无可用的金山云上游账号")
	}
	record := &DomainRecord{ID: snowflake.GenID(), UserID: userID, AccountID: accountID, Domain: input.Domain, BusinessType: input.BusinessType, AccelerateRegion: input.AccelerateRegion, OriginType: input.OriginType, OriginHost: input.OriginHost, OriginProtocol: input.OriginProtocol, OwnershipStatus: "unverified", OwnershipMethod: "dns", UpstreamStatus: "not_created", Enabled: true, CreatedAt: s.now(), UpdatedAt: s.now()}
	if err := s.db.WithContext(ctx).Table(domainsTable).Create(record).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return nil, errors.New("该域名已经接入或正在验证")
		}
		return nil, err
	}
	return record, nil
}

func (s *Service) Domains(ctx context.Context, userID int64) ([]DomainRecord, error) {
	var rows []DomainRecord
	err := s.db.WithContext(ctx).Table(domainsTable).Where("user_id=? AND deleted=0", userID).Order("id DESC").Find(&rows).Error
	return rows, err
}

func (s *Service) AdminDomains(ctx context.Context, keyword string) ([]DomainRecord, error) {
	var rows []DomainRecord
	q := s.db.WithContext(ctx).Table(domainsTable + " d").Select("d.*,c.username").Joins("LEFT JOIN customer.customers c ON c.id=d.user_id").Where("d.deleted=0")
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		like := "%" + keyword + "%"
		q = q.Where("d.domain LIKE ? OR c.username LIKE ? OR d.service_no LIKE ?", like, like, like)
	}
	err := q.Order("d.id DESC").Scan(&rows).Error
	return rows, err
}

func (s *Service) domain(ctx context.Context, id int64) (DomainRecord, error) {
	var row DomainRecord
	err := s.db.WithContext(ctx).Table(domainsTable).Where("id=?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, ErrNotFound
	}
	return row, err
}
func (s *Service) userDomain(ctx context.Context, userID, id int64) (DomainRecord, error) {
	row, err := s.domain(ctx, id)
	if err != nil {
		return row, err
	}
	if row.UserID != userID || row.Deleted {
		return row, ErrForbidden
	}
	return row, nil
}

func (s *Service) DomainDetail(ctx context.Context, userID, id int64, admin bool) (*DomainDetail, error) {
	row, err := s.domain(ctx, id)
	if err != nil {
		return nil, err
	}
	if !admin && (row.UserID != userID || row.Deleted) {
		return nil, ErrForbidden
	}
	configs, err := s.domainConfigViews(ctx, row.ID, "")
	if err != nil {
		return nil, err
	}
	var deployed int64
	s.db.WithContext(ctx).Table(certsTable).Where("domain_id=? AND deploy_status='deployed'", row.ID).Count(&deployed)
	var configOK int64
	s.db.WithContext(ctx).Table(configTasksTable).Where("domain_id=? AND status='success'", row.ID).Count(&configOK)
	return &DomainDetail{DomainRecord: row, Configs: configs, Checklist: map[string]bool{"ownership": row.OwnershipStatus == "verified", "upstream": row.ServiceNo != "" && row.CNAME != "", "certificate": deployed > 0, "configuration": configOK > 0}}, nil
}

func randomToken() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func (s *Service) StartOwnershipVerification(ctx context.Context, userID, domainID int64, method string) (*OwnershipChallenge, error) {
	domain, err := s.userDomain(ctx, userID, domainID)
	if err != nil {
		return nil, err
	}
	if method != "dns" && method != "file" {
		return nil, errors.New("所有权验证方式无效")
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	encrypted, err := cryptoutil.Encrypt(token, s.key)
	if err != nil {
		return nil, err
	}
	challenge := &OwnershipChallenge{ID: snowflake.GenID(), DomainID: domainID, Method: method, DNSName: "_cdnsell-challenge." + domain.Domain, FileURL: "https://" + domain.Domain + "/.well-known/cdnsell-verification.txt", TokenEncrypted: encrypted, Token: token, Status: "unverified", CreatedAt: s.now(), UpdatedAt: s.now()}
	err = s.db.WithContext(ctx).Exec("INSERT INTO "+ownershipChallengesTable+"(id,domain_id,method,dns_name,file_url,token_encrypted,status,last_error,verified_at,created_at,updated_at) VALUES(?,?,?,?,?,?,'unverified','',NULL,?,?) ON DUPLICATE KEY UPDATE id=VALUES(id),method=VALUES(method),dns_name=VALUES(dns_name),file_url=VALUES(file_url),token_encrypted=VALUES(token_encrypted),status='unverified',last_error='',verified_at=NULL,updated_at=VALUES(updated_at)", challenge.ID, domainID, method, challenge.DNSName, challenge.FileURL, encrypted, challenge.CreatedAt, challenge.UpdatedAt).Error
	if err != nil {
		return nil, err
	}
	_ = s.db.WithContext(ctx).Table(domainsTable).Where("id=?", domainID).Updates(map[string]any{"ownership_method": method, "ownership_status": "unverified"}).Error
	return challenge, nil
}

func (s *Service) OwnershipStatus(ctx context.Context, userID, domainID int64) (*OwnershipChallenge, error) {
	if _, err := s.userDomain(ctx, userID, domainID); err != nil {
		return nil, err
	}
	var challenge OwnershipChallenge
	err := s.db.WithContext(ctx).Table(ownershipChallengesTable).Where("domain_id=?", domainID).Take(&challenge).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("请先提交所有权验证")
	}
	if err != nil {
		return nil, err
	}
	if challenge.Status == "verified" {
		return &challenge, nil
	}
	token, err := cryptoutil.Decrypt(challenge.TokenEncrypted, s.key)
	if err != nil {
		return nil, err
	}
	verified := false
	if challenge.Method == "dns" {
		records, e := net.DefaultResolver.LookupTXT(ctx, challenge.DNSName)
		if e != nil {
			err = e
		} else {
			for _, record := range records {
				if strings.TrimSpace(record) == token || strings.TrimSpace(record) == "cdnsell-verification="+token {
					verified = true
					break
				}
			}
			if !verified {
				err = errors.New("尚未找到匹配的 TXT 记录")
			}
		}
	} else {
		verified, err = verifyOwnershipFile(ctx, challenge.FileURL, token)
	}
	if !verified {
		message := "验证记录尚未生效"
		if err != nil {
			message = err.Error()
		}
		_ = s.db.WithContext(ctx).Table(ownershipChallengesTable).Where("domain_id=?", domainID).Updates(map[string]any{"last_error": truncate(message, 255), "updated_at": s.now()}).Error
		challenge.LastError = message
		return &challenge, nil
	}
	now := s.now()
	challenge.Status = "verified"
	challenge.VerifiedAt = &now
	challenge.LastError = ""
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := tx.Table(ownershipChallengesTable).Where("domain_id=?", domainID).Updates(map[string]any{"status": "verified", "last_error": "", "verified_at": now, "updated_at": now}).Error; e != nil {
			return e
		}
		return tx.Table(domainsTable).Where("id=?", domainID).Updates(map[string]any{"ownership_status": "verified", "ownership_method": challenge.Method}).Error
	})
	if err != nil {
		return nil, err
	}
	_, _ = s.enqueueDomainTask(ctx, "domain_add", domainID)
	return &challenge, nil
}

func verifyOwnershipFile(ctx context.Context, fileURL, token string) (bool, error) {
	host := strings.TrimPrefix(strings.Split(strings.TrimPrefix(fileURL, "https://"), "/")[0], "[")
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return false, fmt.Errorf("验证文件域名解析失败: %w", err)
	}
	var target net.IP
	for _, address := range addresses {
		if isPublicIP(address.IP) {
			target = address.IP
			break
		}
	}
	if target == nil {
		return false, errors.New("验证文件域名未解析到公网 IP")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}, DialContext: func(c context.Context, network, address string) (net.Conn, error) {
		_, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		return dialer.DialContext(c, network, net.JoinHostPort(target.String(), port))
	}}
	client := &http.Client{Timeout: 8 * time.Second, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("验证文件地址不允许跳转") }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return false, err
	}
	res, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("读取验证文件失败: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("验证文件返回 HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(body)) == token, nil
}

func truncate(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

func (s *Service) enqueueDomainTask(ctx context.Context, kind string, domainID int64) (*DomainTaskRecord, error) {
	var count int64
	s.db.WithContext(ctx).Table(tasksTable).Where("task_type=? AND biz_type='domain' AND biz_id=? AND status IN ('pending','running')", kind, domainID).Count(&count)
	if count > 0 {
		return nil, errors.New("同类任务正在处理中")
	}
	id := snowflake.GenID()
	task := &DomainTaskRecord{ID: id, TaskNo: fmt.Sprintf("CT%d", id), TaskType: kind, BizType: "domain", BizID: domainID, Status: "pending", MaxAttempts: 5, CreatedAt: s.now()}
	return task, s.db.WithContext(ctx).Table(tasksTable).Create(task).Error
}

func (s *Service) DeleteDomain(ctx context.Context, userID, domainID int64) (*DomainTaskRecord, error) {
	domain, err := s.userDomain(ctx, userID, domainID)
	if err != nil {
		return nil, err
	}
	if domain.UpstreamStatus == "not_created" && domain.ServiceNo == "" {
		err = s.db.WithContext(ctx).Table(domainsTable).Where("id=?", domainID).Update("deleted", true).Error
		return &DomainTaskRecord{BizID: domainID, TaskType: "domain_remove", Status: "success"}, err
	}
	if err = s.db.WithContext(ctx).Table(domainsTable).Where("id=?", domainID).Updates(map[string]any{"deleted": true, "upstream_status": "removing"}).Error; err != nil {
		return nil, err
	}
	return s.enqueueDomainTask(ctx, "domain_remove", domainID)
}

func (s *Service) SetDomainState(ctx context.Context, userID, domainID int64, enabled bool) (*DomainTaskRecord, error) {
	domain, err := s.userDomain(ctx, userID, domainID)
	if err != nil {
		return nil, err
	}
	if domain.ServiceNo == "" || domain.UpstreamStatus != "active" {
		return nil, errors.New("域名尚未完成上游接入")
	}
	kind := "domain_disable"
	if enabled {
		kind = "domain_enable"
	}
	return s.enqueueDomainTask(ctx, kind, domainID)
}

func (s *Service) SyncDomain(ctx context.Context, userID, domainID int64, admin bool) (*DomainTaskRecord, error) {
	domain, err := s.domain(ctx, domainID)
	if err != nil {
		return nil, err
	}
	if !admin && domain.UserID != userID {
		return nil, ErrForbidden
	}
	if domain.ServiceNo == "" {
		return nil, errors.New("域名尚未完成上游接入")
	}
	return s.enqueueDomainTask(ctx, "domain_sync", domainID)
}
