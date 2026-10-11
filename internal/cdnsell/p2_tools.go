package cdnsell

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"ddyidc/internal/shared/snowflake"
	cryptoutil "ddyidc/pkg/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) UploadCertificate(ctx context.Context, userID, domainID int64, name, certPEM, keyPEM string) (*CertificateRecord, error) {
	domain, err := s.userDomain(ctx, userID, domainID)
	if err != nil {
		return nil, err
	}
	if len(name) > 120 || len(certPEM) > 128*1024 || len(keyPEM) > 128*1024 {
		return nil, errors.New("证书名称或内容超过允许长度")
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("证书名称不能为空")
	}
	if _, err = tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return nil, errors.New("证书与私钥不匹配或 PEM 格式无效")
	}
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, errors.New("证书 PEM 内容无效")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("无法解析证书内容")
	}
	if err = certificate.VerifyHostname(domain.Domain); err != nil {
		return nil, errors.New("证书不适用于所选加速域名")
	}
	if s.now().Before(certificate.NotBefore) || !s.now().Before(certificate.NotAfter) {
		return nil, errors.New("证书尚未生效或已经过期")
	}
	digest := sha256.Sum256(certificate.Raw)
	certEncrypted, err := cryptoutil.Encrypt(certPEM, s.key)
	if err != nil {
		return nil, err
	}
	keyEncrypted, err := cryptoutil.Encrypt(keyPEM, s.key)
	if err != nil {
		return nil, err
	}
	record := &CertificateRecord{ID: snowflake.GenID(), UserID: userID, DomainID: domainID, Name: strings.TrimSpace(name), Fingerprint: hex.EncodeToString(digest[:]), CertEncrypted: certEncrypted, KeyEncrypted: keyEncrypted, NotBefore: certificate.NotBefore, NotAfter: certificate.NotAfter, DeployStatus: "undepolyed", CreatedAt: s.now()}
	if err = s.db.WithContext(ctx).Table(certsTable).Create(record).Error; err != nil {
		return nil, err
	}
	return record, nil
}

func (s *Service) Certificates(ctx context.Context, userID int64) ([]CertificateRecord, error) {
	var rows []CertificateRecord
	err := s.db.WithContext(ctx).Table(certsTable+" c").Select("c.id,c.user_id,c.domain_id,c.name,c.fingerprint,c.not_before,c.not_after,c.deploy_status,c.created_at,d.domain").Joins("LEFT JOIN "+domainsTable+" d ON d.id=c.domain_id").Where("c.user_id=?", userID).Order("c.id DESC").Scan(&rows).Error
	return rows, err
}
func (s *Service) DeleteCertificate(ctx context.Context, userID, certID int64) error {
	result := s.db.WithContext(ctx).Table(certsTable).Where("id=? AND user_id=? AND deploy_status NOT IN ('pending','deployed')", certID, userID).Delete(nil)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("仅可删除未部署且非处理中的证书")
	}
	return nil
}
func (s *Service) DeployCertificate(ctx context.Context, userID, certID int64) (task *DomainTaskRecord, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cert CertificateRecord
		if e := tx.Table(certsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=?", certID, userID).Take(&cert).Error; e != nil {
			return e
		}
		if cert.DeployStatus == "pending" || cert.DeployStatus == "deployed" {
			return errors.New("证书正在核验或已部署，请勿重复提交")
		}
		var domain DomainRecord
		if e := tx.Table(domainsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND user_id=? AND deleted=0", cert.DomainID, userID).Take(&domain).Error; e != nil {
			return e
		}
		if domain.ServiceNo == "" || domain.UpstreamStatus != "active" {
			return errors.New("域名尚未完成服务接入")
		}
		scoped := &Service{db: tx, now: s.now}
		var e error
		task, e = scoped.enqueueTask(ctx, "cert_deploy", "certificate", certID)
		if e != nil {
			return e
		}
		return tx.Table(certsTable).Where("id=?", certID).Update("deploy_status", "pending").Error
	})
	return
}
func (s *Service) enqueueTask(ctx context.Context, kind, bizType string, bizID int64) (*DomainTaskRecord, error) {
	var count int64
	if e := s.db.WithContext(ctx).Table(tasksTable).Where("task_type=? AND biz_type=? AND biz_id=? AND status IN ('pending','running')", kind, bizType, bizID).Count(&count).Error; e != nil {
		return nil, e
	}
	if count > 0 {
		return nil, errors.New("同类任务正在处理中")
	}
	id := snowflake.GenID()
	task := &DomainTaskRecord{ID: id, TaskNo: fmt.Sprintf("CT%d", id), TaskType: kind, BizType: bizType, BizID: bizID, Status: "pending", MaxAttempts: 5, CreatedAt: s.now()}
	return task, s.db.WithContext(ctx).Table(tasksTable).Create(task).Error
}

func (s *Service) SubmitRefresh(ctx context.Context, userID int64, kind string, urls []string) (*RefreshTaskRecord, error) {
	if kind != "url_refresh" && kind != "dir_refresh" && kind != "url_purge" {
		return nil, errors.New("刷新类型无效")
	}
	if len(urls) == 0 || len(urls) > 1000 {
		return nil, errors.New("资源地址数量必须为 1–1000 条")
	}
	var domainID int64
	normalized := make([]string, 0, len(urls))
	for _, raw := range urls {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
			return nil, errors.New("资源地址必须是有效的 HTTP/HTTPS URL")
		}
		host, err := normalizeDomain(parsed.Hostname())
		if err != nil {
			return nil, errors.New("资源地址主机名无效")
		}
		var domain DomainRecord
		err = s.db.WithContext(ctx).Table(domainsTable).Where("user_id=? AND domain=? AND deleted=0", userID, host).Take(&domain).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrForbidden
		}
		if err != nil {
			return nil, err
		}
		if domainID != 0 && domainID != domain.ID {
			return nil, errors.New("单个任务中的 URL 必须属于同一个加速域名")
		}
		if domain.ServiceNo == "" || domain.UpstreamStatus != "active" {
			return nil, errors.New("资源地址所属域名尚未完成接入")
		}
		domainID = domain.ID
		parsed.Fragment = ""
		normalized = append(normalized, parsed.String())
	}
	data, _ := json.Marshal(normalized)
	progress := json.RawMessage(`{"percent":0,"cloudTaskId":""}`)
	id := snowflake.GenID()
	task := &RefreshTaskRecord{ID: id, TaskNo: fmt.Sprintf("CR%d", id), UserID: userID, DomainID: domainID, Type: kind, URLs: data, Status: "pending", Progress: progress, CreatedAt: s.now()}
	return task, s.db.WithContext(ctx).Table(refreshTasksTable).Create(task).Error
}

func (s *Service) RefreshTasks(ctx context.Context, userID int64) ([]RefreshTaskRecord, error) {
	var rows []RefreshTaskRecord
	err := s.db.WithContext(ctx).Table(refreshTasksTable+" r").Select("r.*,d.domain").Joins("LEFT JOIN "+domainsTable+" d ON d.id=r.domain_id").Where("r.user_id=?", userID).Order("r.id DESC").Limit(100).Scan(&rows).Error
	return rows, err
}

func (s *Service) LogDownloads(ctx context.Context, userID, domainID int64, start, end time.Time) ([]LogDownload, error) {
	domain, err := s.userDomain(ctx, userID, domainID)
	if err != nil {
		return nil, err
	}
	if domain.ServiceNo == "" {
		return nil, errors.New("域名尚未完成接入")
	}
	if end.Before(start) || end.Sub(start) > 33*24*time.Hour {
		return nil, errors.New("日志查询时间范围必须在33天以内")
	}
	_, account, err := s.accountSecret(ctx, domain.AccountID)
	if err != nil {
		return nil, err
	}
	called := s.now()
	if err = s.waitCloudGap(ctx); err != nil {
		return nil, err
	}
	rows, err := s.cloud.GetLogs(ctx, account, domain.ServiceNo, start, end)
	s.writeAPILog(ctx, domain.AccountID, "GetLogs", map[string]any{"domainId": domain.ID, "start": start, "end": end}, map[string]any{"count": len(rows)}, called, err)
	if err != nil {
		return nil, err
	}
	downloads := make([]LogDownload, 0, len(rows))
	for _, row := range rows {
		raw, _ := row["url"].(string)
		parsed, e := url.Parse(raw)
		if e != nil || parsed.Scheme != "https" {
			continue
		}
		downloads = append(downloads, LogDownload{DomainID: domain.ID, Start: parseCloudTime(row["start"]), End: parseCloudTime(row["end"]), Name: stringValue(row["name"]), URL: raw, Size: numeric(row["size"])})
	}
	return downloads, nil
}

func parseCloudTime(value any) time.Time {
	raw := stringValue(value)
	for _, layout := range []string{"2006-01-02T15:04-0700", "2006-01-02 15:04:05", "2006-01-02", time.RFC3339} {
		if parsed, err := time.ParseInLocation(layout, raw, time.FixedZone("UTC+8", 8*60*60)); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func (s *Service) CheckIP(ctx context.Context, ipText string) (map[string]any, error) {
	ip := net.ParseIP(strings.TrimSpace(ipText))
	if ip == nil {
		return nil, errors.New("IP 地址格式无效")
	}
	var accountID int64
	if err := s.db.WithContext(ctx).Table(accountsTable).Select("id").Where("status=1").Order("sort,id").Limit(1).Scan(&accountID).Error; err != nil {
		return nil, err
	}
	if accountID == 0 {
		return nil, errors.New("暂无可用的金山云上游账号")
	}
	_, account, err := s.accountSecret(ctx, accountID)
	if err != nil {
		return nil, err
	}
	called := s.now()
	if err = s.waitCloudGap(ctx); err != nil {
		return nil, err
	}
	result, err := s.cloud.CheckIP(ctx, account, ip.String())
	s.writeAPILog(ctx, accountID, "CheckIP", map[string]any{"ip": ip.String()}, result, called, err)
	return result, err
}

func (s *Service) writeAPILog(ctx context.Context, accountID int64, api string, request, response any, started time.Time, callErr error) {
	requestJSON, _ := json.Marshal(request)
	responseJSON, _ := json.Marshal(response)
	success := callErr == nil
	errorText := ""
	if callErr != nil {
		errorText = truncate(callErr.Error(), 2000)
	}
	_ = s.db.WithContext(ctx).Exec("INSERT INTO system.cdnsell_api_logs(id,account_id,api_name,request,response,status_code,cost_ms,success,error) VALUES(?,?,?,?,?,?,?,?,?)", snowflake.GenID(), accountID, api, requestJSON, responseJSON, map[bool]int{true: 200, false: 0}[success], time.Since(started).Milliseconds(), success, errorText).Error
}

func (s *Service) RunCertificateExpiry(ctx context.Context) error {
	enabled, err := s.Enabled(ctx)
	if err != nil || !enabled {
		return err
	}
	var settings struct {
		Days     int             `gorm:"column:cert_expire_days"`
		Channels json.RawMessage `gorm:"column:notify_channels"`
		Cooldown int             `gorm:"column:overage_notify_interval"`
	}
	if err := s.db.WithContext(ctx).Table(settingsTable).Where("id=1").Take(&settings).Error; err != nil {
		return err
	}
	if settings.Days <= 0 {
		settings.Days = 30
	}
	var channels map[string]bool
	if err := json.Unmarshal(settings.Channels, &channels); err != nil {
		return err
	}
	var rows []CertificateRecord
	if err := s.db.WithContext(ctx).Table(certsTable).Where("not_after BETWEEN ? AND ?", s.now(), s.now().AddDate(0, 0, settings.Days)).Order("user_id,not_after,id").Find(&rows).Error; err != nil {
		return err
	}
	groups := map[int64][]CertificateRecord{}
	for _, cert := range rows {
		groups[cert.UserID] = append(groups[cert.UserID], cert)
	}
	var failures []error
	for user, certs := range groups {
		first := certs[0]
		content := fmt.Sprintf("您有 %d 个 CDN 证书即将到期，最早到期日期 %s，请及时更新。", len(certs), first.NotAfter.Format("2006-01-02"))
		if err := s.sendCDNWarning(ctx, user, "certificate_expiry", "cdnsell_cert_expiry", content, map[string]string{"name": fmt.Sprintf("%d 个证书", len(certs)), "expireAt": first.NotAfter.Format("2006-01-02")}, channels, settings.Cooldown); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func strconvID(id int64) string { return fmt.Sprintf("%d", id) }
