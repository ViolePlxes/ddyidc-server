package cdnsell

import (
	"context"
	"ddyidc/internal/shared/distsync"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ddyidc/internal/cdnsell/provider"
	cryptoutil "ddyidc/pkg/crypto"
	"gorm.io/gorm"
)

func waitUpstream(ctx context.Context) error {
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) P2CloudWritesEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.db.WithContext(ctx).Table("system.cdnsell_p2_runtime").Select("cloud_writes_enabled").Where("id=1").Scan(&enabled).Error
	return enabled, err
}

func (s *Service) RunP2Tasks(ctx context.Context) error {
	enabled, err := s.P2CloudWritesEnabled(ctx)
	if err != nil || !enabled {
		return err
	}
	if err = s.processDomainTask(ctx); err != nil {
		return err
	}
	if err = s.processConfigTask(ctx); err != nil {
		return err
	}
	return s.processRefreshTask(ctx)
}

func (s *Service) processDomainTask(ctx context.Context) error {
	var task DomainTaskRecord
	err := s.db.WithContext(ctx).Table(tasksTable).Where("status IN ('pending','failed') AND attempts<max_attempts AND (next_run_at IS NULL OR next_run_at<=?)", s.now()).Order("id").Take(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table(tasksTable).Where("id=? AND status IN ('pending','failed')", task.ID).Updates(map[string]any{"status": "running", "attempts": gorm.Expr("attempts+1"), "error": ""})
	if result.Error != nil || result.RowsAffected == 0 {
		return result.Error
	}
	task.Attempts++
	err = s.executeDomainTask(ctx, task)
	if err == nil {
		return s.db.WithContext(ctx).Table(tasksTable).Where("id=?", task.ID).Updates(map[string]any{"status": "success", "finished_at": s.now(), "next_run_at": nil}).Error
	}
	status := "failed"
	if task.Attempts >= task.MaxAttempts {
		status = "need_manual"
	}
	_ = s.db.WithContext(ctx).Table(tasksTable).Where("id=?", task.ID).Updates(map[string]any{"status": status, "error": truncate(err.Error(), 2000), "next_run_at": s.now().Add(time.Duration(task.Attempts) * time.Minute)}).Error
	return nil
}

func (s *Service) executeDomainTask(ctx context.Context, task DomainTaskRecord) error {
	if task.TaskType == "cert_deploy" {
		return s.executeCertDeploy(ctx, task.BizID)
	}
	domain, err := s.domain(ctx, task.BizID)
	if err != nil {
		return err
	}
	_, account, err := s.accountSecret(ctx, domain.AccountID)
	if err != nil {
		return err
	}
	started := s.now()
	switch task.TaskType {
	case "domain_add":
		if domain.OwnershipStatus != "verified" {
			return errors.New("域名所有权尚未验证")
		}
		serviceNo := domain.ServiceNo
		if serviceNo == "" {
			if err = s.waitCloudGap(ctx); err != nil {
				return err
			}
			serviceNo, err = s.cloud.AddDomain(ctx, account, provider.DomainInput{Domain: domain.Domain, BusinessType: domain.BusinessType, Region: domain.AccelerateRegion, OriginType: domain.OriginType, OriginHost: domain.OriginHost, OriginProtocol: domain.OriginProtocol})
			s.writeAPILog(ctx, domain.AccountID, "AddDomain", map[string]any{"domainId": domain.ID, "businessType": domain.BusinessType, "region": domain.AccelerateRegion, "originType": domain.OriginType, "originProtocol": domain.OriginProtocol}, map[string]any{"serviceNo": serviceNo}, started, err)
			if err != nil {
				_ = s.db.WithContext(ctx).Table(domainsTable).Where("id=?", domain.ID).Update("upstream_status", "failed").Error
				return err
			}
			if err = s.db.WithContext(ctx).Table(domainsTable).Where("id=?", domain.ID).Updates(map[string]any{"service_no": serviceNo, "upstream_status": "creating"}).Error; err != nil {
				return err
			}
			domain.ServiceNo = serviceNo
			_ = s.updateCapability(ctx, "AddDomain", true, "真实调用成功")
		}
		if err = waitUpstream(ctx); err != nil {
			return err
		}
		return s.syncDomainFromCloud(ctx, domain, account)
	case "domain_remove":
		if err = s.waitCloudGap(ctx); err != nil {
			return err
		}
		err = s.cloud.RemoveDomain(ctx, account, domain.ServiceNo)
		s.writeAPILog(ctx, domain.AccountID, "RemoveDomain", map[string]any{"domainId": domain.ID}, map[string]any{}, started, err)
		if err == nil {
			_ = s.updateCapability(ctx, "RemoveDomain", true, "真实调用成功")
			return s.db.WithContext(ctx).Table(domainsTable).Where("id=?", domain.ID).Updates(map[string]any{"upstream_status": "not_created", "service_no": "", "cname": "", "node_status": "", "enabled": false}).Error
		}
		return err
	case "domain_enable", "domain_disable":
		enabled := task.TaskType == "domain_enable"
		if err = s.waitCloudGap(ctx); err != nil {
			return err
		}
		err = s.cloud.SetDomainEnabled(ctx, account, domain.ServiceNo, enabled)
		s.writeAPILog(ctx, domain.AccountID, "SetDomainEnabled", map[string]any{"domainId": domain.ID, "enabled": enabled}, map[string]any{}, started, err)
		if err == nil {
			_ = s.updateCapability(ctx, "SetDomainEnabled", true, "真实调用成功")
			return s.db.WithContext(ctx).Table(domainsTable).Where("id=?", domain.ID).Update("enabled", enabled).Error
		}
		return err
	case "domain_sync":
		return s.syncDomainFromCloud(ctx, domain, account)
	default:
		return errors.New("未知 CDN 域名任务")
	}
}

func (s *Service) syncDomainFromCloud(ctx context.Context, domain DomainRecord, account provider.Account) error {
	started := s.now()
	if err := s.waitCloudGap(ctx); err != nil {
		return err
	}
	cloud, err := s.cloud.GetDomain(ctx, account, domain.ServiceNo)
	s.writeAPILog(ctx, domain.AccountID, "GetDomain", map[string]any{"domainId": domain.ID}, map[string]any{"status": cloud["status"]}, started, err)
	if err != nil {
		return err
	}
	status := stringValue(cloud["status"])
	upstream := "creating"
	if strings.EqualFold(status, "online") || strings.EqualFold(status, "active") || strings.EqualFold(status, "offline") {
		upstream = "active"
	}
	updates := map[string]any{"upstream_status": upstream, "node_status": status, "sync_version": gorm.Expr("sync_version+1"), "sync_time": s.now()}
	if cname := stringValue(cloud["cname"]); cname != "" {
		updates["cname"] = cname
	}
	if err = s.db.WithContext(ctx).Table(domainsTable).Where("id=?", domain.ID).Updates(updates).Error; err != nil {
		return err
	}
	_ = s.updateCapability(ctx, "GetDomain", true, "真实回读成功")
	views, err := s.domainConfigViews(ctx, domain.ID, "")
	if err != nil {
		return err
	}
	for _, view := range views {
		if view.LocalVersion == 0 || view.Available == nil || !*view.Available {
			continue
		}
		if err = waitUpstream(ctx); err != nil {
			return err
		}
		if syncErr := s.syncDomainConfig(ctx, domain, view.ConfigModule, 0); syncErr != nil && !errors.Is(syncErr, provider.ErrCapabilityNotOpen) {
			return syncErr
		}
	}
	return nil
}

func (s *Service) executeCertDeploy(ctx context.Context, certID int64) error {
	var cert CertificateRecord
	if err := s.db.WithContext(ctx).Table(certsTable).Where("id=?", certID).Take(&cert).Error; err != nil {
		return err
	}
	domain, err := s.domain(ctx, cert.DomainID)
	if err != nil {
		return err
	}
	_, account, err := s.accountSecret(ctx, domain.AccountID)
	if err != nil {
		return err
	}
	certificate, err := cryptoutil.Decrypt(cert.CertEncrypted, s.key)
	if err != nil {
		return err
	}
	privateKey, err := cryptoutil.Decrypt(cert.KeyEncrypted, s.key)
	if err != nil {
		return err
	}
	started := s.now()
	if err = s.waitCloudGap(ctx); err != nil {
		return err
	}
	err = s.cloud.DeployCertificate(ctx, account, domain.ServiceNo, certificate, privateKey)
	certificate, privateKey = "", ""
	s.writeAPILog(ctx, domain.AccountID, "DeployCertificate", map[string]any{"domainId": domain.ID, "certificateId": cert.ID}, map[string]any{}, started, err)
	status := "deployed"
	if err != nil {
		status = "failed"
	}
	_ = s.db.WithContext(ctx).Table(certsTable).Where("id=?", cert.ID).Update("deploy_status", status).Error
	if err == nil {
		_ = s.updateCapability(ctx, "DeployCertificate", true, "真实调用成功")
	}
	return err
}

func (s *Service) processConfigTask(ctx context.Context) error {
	var task ConfigTaskRecord
	err := s.db.WithContext(ctx).Table(configTasksTable).Where("status IN ('pending','failed') AND attempts<max_attempts").Order("id").Take(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	result := s.db.WithContext(ctx).Table(configTasksTable).Where("id=? AND status IN ('pending','failed')", task.ID).Updates(map[string]any{"status": "running", "attempts": gorm.Expr("attempts+1"), "error": ""})
	if result.Error != nil || result.RowsAffected == 0 {
		return result.Error
	}
	task.Attempts++
	plain, err := cryptoutil.Decrypt(task.Payload, s.key)
	if err == nil {
		var config map[string]any
		err = json.Unmarshal([]byte(plain), &config)
		plain = ""
		if err == nil {
			domain, loadErr := s.domain(ctx, task.DomainID)
			if loadErr != nil {
				err = loadErr
			} else {
				_, account, secretErr := s.accountSecret(ctx, domain.AccountID)
				if secretErr != nil {
					err = secretErr
				} else {
					started := s.now()
					if err = s.waitCloudGap(ctx); err == nil {
						err = s.cloud.SetDomainConfig(ctx, account, domain.ServiceNo, task.ConfigModule, config)
						s.writeAPILog(ctx, domain.AccountID, "SetDomainConfig", map[string]any{"domainId": domain.ID, "module": task.ConfigModule, "version": task.Version}, map[string]any{}, started, err)
					}
					config = nil
					if err == nil {
						if err = waitUpstream(ctx); err == nil {
							err = s.syncDomainConfig(ctx, domain, task.ConfigModule, task.Version)
						}
					}
				}
			}
		}
	}
	if err == nil {
		_ = s.updateCapability(ctx, "SetDomainConfig", true, "真实下发与回读成功")
		return s.db.WithContext(ctx).Table(configTasksTable).Where("id=?", task.ID).Updates(map[string]any{"status": "success", "finished_at": s.now()}).Error
	}
	if errors.Is(err, provider.ErrCapabilityNotOpen) {
		_ = s.updateCapability(ctx, "SetDomainConfig", false, "当前 SDK 未开放该配置写接口")
	}
	status := "failed"
	if task.Attempts >= task.MaxAttempts {
		status = "terminated"
	}
	_ = s.db.WithContext(ctx).Table(configTasksTable).Where("id=?", task.ID).Updates(map[string]any{"status": status, "error": truncate(err.Error(), 2000)}).Error
	return nil
}

func (s *Service) processRefreshTask(ctx context.Context) error {
	var task RefreshTaskRecord
	err := s.db.WithContext(ctx).Table(refreshTasksTable).Where("status IN ('pending','running')").Order("id").Take(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	domain, err := s.domain(ctx, task.DomainID)
	if err != nil {
		return err
	}
	_, account, err := s.accountSecret(ctx, domain.AccountID)
	if err != nil {
		return err
	}
	var progress map[string]any
	_ = json.Unmarshal(task.Progress, &progress)
	cloudTaskID := stringValue(progress["cloudTaskId"])
	if cloudTaskID == "" {
		var urls []string
		if err = json.Unmarshal(task.URLs, &urls); err != nil {
			return err
		}
		started := s.now()
		if err = s.waitCloudGap(ctx); err != nil {
			return err
		}
		cloudTaskID, err = s.cloud.SubmitRefresh(ctx, account, task.Type, urls)
		s.writeAPILog(ctx, domain.AccountID, "SubmitRefresh", map[string]any{"domainId": domain.ID, "type": task.Type, "count": len(urls)}, map[string]any{"taskId": cloudTaskID}, started, err)
		if err != nil {
			_ = s.db.WithContext(ctx).Table(refreshTasksTable).Where("id=?", task.ID).Updates(map[string]any{"status": "failed", "error": truncate(err.Error(), 2000), "finished_at": s.now()}).Error
			return nil
		}
		payload, _ := json.Marshal(map[string]any{"percent": 0, "cloudTaskId": cloudTaskID})
		_ = s.updateCapability(ctx, "SubmitRefresh", true, "真实提交成功")
		return s.db.WithContext(ctx).Table(refreshTasksTable).Where("id=?", task.ID).Updates(map[string]any{"status": "running", "progress": payload}).Error
	}
	started := s.now()
	if err = s.waitCloudGap(ctx); err != nil {
		return err
	}
	cloud, err := s.cloud.GetRefreshProgress(ctx, account, cloudTaskID)
	s.writeAPILog(ctx, domain.AccountID, "GetRefreshProgress", map[string]any{"domainId": domain.ID, "taskId": cloudTaskID}, cloud, started, err)
	if err != nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"percent": cloud["progress"], "cloudTaskId": cloudTaskID, "cloudStatus": cloud["status"]})
	statusText := strings.ToLower(stringValue(cloud["status"]))
	updates := map[string]any{"progress": payload}
	if strings.Contains(statusText, "success") || strings.Contains(statusText, "complete") || numeric(cloud["progress"]) >= 100 {
		updates["status"] = "success"
		updates["finished_at"] = s.now()
		_ = s.updateCapability(ctx, "GetRefreshProgress", true, "真实进度查询成功")
	} else if strings.Contains(statusText, "fail") {
		updates["status"] = "failed"
		updates["error"] = "金山云刷新任务失败"
		updates["finished_at"] = s.now()
	}
	return s.db.WithContext(ctx).Table(refreshTasksTable).Where("id=?", task.ID).Updates(updates).Error
}

func (s *Service) updateCapability(ctx context.Context, api string, available bool, note string) error {
	return s.db.WithContext(ctx).Table("system.cdnsell_api_capabilities").Where("api_name=?", api).Updates(map[string]any{"available": available, "note": note, "checked_at": s.now()}).Error
}

func (s *Service) scheduleDomainSync(ctx context.Context) error {
	enabled, err := s.P2CloudWritesEnabled(ctx)
	if err != nil || !enabled {
		return err
	}
	var ids []int64
	if err = s.db.WithContext(ctx).Table(domainsTable).Where("deleted=0 AND service_no<>''").Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		_, _ = s.enqueueDomainTask(ctx, "domain_sync", id)
	}
	return nil
}

func (s *Service) startP2(ctx context.Context) {
	go func() {
		worker := time.NewTicker(3 * time.Second)
		syncTicker := time.NewTicker(10 * time.Minute)
		certTicker := time.NewTicker(24 * time.Hour)
		defer worker.Stop()
		defer syncTicker.Stop()
		defer certTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-worker.C:
				if distsync.IsLeader(ctx) {
					_ = distsync.Work(ctx, s.RunP2Tasks)
				}
			case <-syncTicker.C:
				distsync.Task(ctx, "cdn_domain_periodic_sync", time.Minute, func(ctx context.Context) error {
					s.runTask(ctx, "domain_periodic_sync", s.scheduleDomainSync)
					return nil
				})
			case <-certTicker.C:
				distsync.Task(ctx, "cdn_certificate_expiry", time.Minute, func(ctx context.Context) error {
					s.runTask(ctx, "certificate_expiry", s.RunCertificateExpiry)
					return nil
				})
			}
		}
	}()
}
