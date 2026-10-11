package cdnsell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ddyidc/internal/cdnsell/provider"
	"ddyidc/internal/shared/snowflake"
	cryptoutil "ddyidc/pkg/crypto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var authKeyPattern = regexp.MustCompile(`^[A-Za-z0-9]{6,128}$`)

func emptyJSON() json.RawMessage { return json.RawMessage(`{}`) }

func (s *Service) domainConfigViews(ctx context.Context, domainID int64, module string) ([]ConfigView, error) {
	var rows []DomainConfigRecord
	q := s.db.WithContext(ctx).Table(domainConfigsTable).Where("domain_id=?", domainID)
	if module != "" {
		q = q.Where("config_module=?", module)
	}
	if err := q.Order("config_module").Find(&rows).Error; err != nil {
		return nil, err
	}
	byKey := make(map[string]DomainConfigRecord, len(rows))
	for _, row := range rows {
		byKey[row.ConfigModule] = row
	}
	views := make([]ConfigView, 0, len(configModuleDefinitions))
	for _, definition := range configModuleDefinitions {
		if module != "" && definition.Key != module {
			continue
		}
		row, ok := byKey[definition.Key]
		if !ok {
			row = DomainConfigRecord{DomainID: domainID, ConfigModule: definition.Key, LocalConfig: emptyJSON(), CloudConfig: emptyJSON(), DiffFields: json.RawMessage(`[]`)}
		}
		var fields []string
		_ = json.Unmarshal(row.DiffFields, &fields)
		views = append(views, ConfigView{DomainConfigRecord: row, Available: definition.Available, Note: definition.Note, DiffCount: len(fields)})
	}
	return views, nil
}

func (s *Service) DomainConfig(ctx context.Context, userID, domainID int64, module string, admin bool) ([]ConfigView, error) {
	domain, err := s.domain(ctx, domainID)
	if err != nil {
		return nil, err
	}
	if !admin && domain.UserID != userID {
		return nil, ErrForbidden
	}
	if module != "" {
		if _, ok := moduleDefinition(module); !ok {
			return nil, errors.New("配置模块无效")
		}
	}
	return s.domainConfigViews(ctx, domainID, module)
}

func validateConfig(module string, config map[string]any) error {
	definition, ok := moduleDefinition(module)
	if !ok {
		return errors.New("配置模块无效")
	}
	if definition.Available != nil && !*definition.Available {
		return provider.ErrCapabilityNotOpen
	}
	switch module {
	case "cache_rules":
		rows, ok := config["rules"].([]any)
		if !ok || len(rows) == 0 || len(rows) > 100 {
			return errors.New("请填写1–100条缓存规则")
		}
		for _, raw := range rows {
			row, ok := raw.(map[string]any)
			if !ok {
				return errors.New("缓存规则格式无效")
			}
			kind, _ := row["type"].(string)
			value, _ := row["value"].(string)
			if value == "" || strings.ContainsAny(value, "\r\n") {
				return errors.New("缓存匹配值不能为空或包含换行")
			}
			switch kind {
			case "directory":
				if !strings.HasPrefix(value, "/") || !strings.HasSuffix(value, "/") {
					return errors.New("目录缓存路径必须以/开头和结尾")
				}
			case "exact":
				if !strings.HasPrefix(value, "/") {
					return errors.New("精确缓存路径必须以/开头")
				}
			case "file_suffix":
			default:
				return errors.New("缓存类型只能为directory、exact或file_suffix")
			}
			enabled, ok := row["enabled"].(bool)
			if !ok {
				return errors.New("缓存开关格式无效")
			}
			if enabled {
				seconds, ok := row["seconds"].(float64)
				if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) || math.Trunc(seconds) != seconds || seconds < 0 || seconds > 31536000 {
					return errors.New("缓存秒数必须为0–31536000的整数")
				}
			}
			if _, ok := row["respectOrigin"].(bool); !ok {
				return errors.New("遵循源站开关格式无效")
			}
		}
	case "http_headers":
		name, _ := config["name"].(string)
		value, _ := config["value"].(string)
		if !regexp.MustCompile(`^[!#$%&'*+.^_`+"`"+`|~0-9A-Za-z-]+$`).MatchString(name) || strings.ContainsAny(value, "\r\n") {
			return errors.New("HTTP头名称或值无效")
		}
	case "performance":
		for _, key := range []string{"gzip", "br"} {
			if _, ok := config[key].(bool); !ok {
				return errors.New("压缩开关必须为布尔值")
			}
		}
		value, _ := config["fileSize"].(string)
		if value == "" {
			return errors.New("请填写压缩文件大小范围，例如1024-31457280")
		}
		parts := strings.Split(value, "-")
		if len(parts) != 2 {
			return errors.New("压缩大小范围格式无效")
		}
		low, e1 := strconv.ParseInt(parts[0], 10, 64)
		high, e2 := strconv.ParseInt(parts[1], 10, 64)
		if e1 != nil || e2 != nil || low < 0 || high < low || high > 31457280 {
			return errors.New("压缩大小范围必须为1024–31457280字节且起点不大于终点")
		}
	case "request_auth":
		enabled, _ := config["enabled"].(bool)
		if enabled {
			kind, _ := config["authType"].(string)
			if kind != "1" && kind != "2" {
				return errors.New("鉴权类型只能为 1 或 2")
			}
			primary, _ := config["primaryKey"].(string)
			backup, _ := config["backupKey"].(string)
			if !authKeyPattern.MatchString(primary) {
				return errors.New("主密钥必须为6–128位英文字母或数字")
			}
			if backup != "" && (!authKeyPattern.MatchString(backup)) {
				return errors.New("备用密钥必须为6–128位英文字母或数字")
			}
			if value, ok := config["expireSeconds"].(float64); ok && math.Trunc(value) != value {
				return errors.New("鉴权过期秒数必须为整数")
			}
			expire := numeric(config["expireSeconds"])
			if expire < 1 || expire > 31536000 {
				return errors.New("鉴权过期秒数必须为 1–31536000")
			}
		}
	case "tls_version":
		values, _ := config["versions"].([]any)
		if len(values) == 0 {
			if typed, yes := config["versions"].([]string); !yes || len(typed) == 0 {
				return errors.New("至少保留一个 TLS 版本")
			}
		}
	case "url_block":
		if err := validateURLStrings(config["urls"]); err != nil {
			return err
		}
	}
	return nil
}

func numeric(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}
func validateURLStrings(value any) error {
	var values []string
	if raw, ok := value.([]any); ok {
		for _, item := range raw {
			if v, yes := item.(string); yes {
				values = append(values, v)
			}
		}
	} else if raw, ok := value.([]string); ok {
		values = raw
	}
	if len(values) == 0 {
		return errors.New("至少填写一个 URL")
	}
	for _, value := range values {
		parsed, err := url.Parse(value)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
			return errors.New("URL 格式无效")
		}
	}
	return nil
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "_", ""))
	return key == "key1" || key == "key2" || strings.Contains(key, "accesskey") || strings.Contains(key, "token") || strings.Contains(key, "privatekey") || strings.Contains(key, "primarykey") || strings.Contains(key, "backupkey") || strings.Contains(key, "secret")
}
func sanitizeConfig(value any) any {
	switch item := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(item))
		for key, val := range item {
			if sensitiveKey(key) {
				if text, ok := val.(string); ok && text != "" {
					out[key] = "••••••"
				} else {
					out[key] = val
				}
				continue
			}
			out[key] = sanitizeConfig(val)
		}
		return out
	case []any:
		out := make([]any, len(item))
		for i, val := range item {
			out[i] = sanitizeConfig(val)
		}
		return out
	default:
		return value
	}
}

func (s *Service) SaveDomainConfig(ctx context.Context, userID, domainID int64, module, operator string, config map[string]any, admin bool) (*ConfigTaskRecord, error) {
	if (admin && operator != "admin") || (!admin && operator != "member") {
		return nil, errors.New("配置操作人类型无效")
	}
	domain, err := s.domain(ctx, domainID)
	if err != nil {
		return nil, err
	}
	if !admin && domain.UserID != userID {
		return nil, ErrForbidden
	}
	if domain.OwnershipStatus != "verified" {
		return nil, errors.New("域名所有权验证通过后才能下发配置")
	}
	if domain.ServiceNo == "" {
		return nil, errors.New("域名尚未完成上游接入")
	}
	if err = validateConfig(module, config); err != nil {
		return nil, err
	}
	plain, err := json.Marshal(config)
	if err != nil {
		return nil, errors.New("配置内容无法序列化")
	}
	encrypted, err := cryptoutil.Encrypt(string(plain), s.key)
	if err != nil {
		return nil, err
	}
	safe, err := json.Marshal(sanitizeConfig(config))
	if err != nil {
		return nil, err
	}
	var task *ConfigTaskRecord
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current DomainConfigRecord
		e := tx.Table(domainConfigsTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("domain_id=? AND config_module=?", domainID, module).Take(&current).Error
		if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		version := current.LocalVersion + 1
		if version < 1 {
			version = 1
		}
		if errors.Is(e, gorm.ErrRecordNotFound) {
			record := DomainConfigRecord{ID: snowflake.GenID(), DomainID: domainID, ConfigModule: module, LocalVersion: version, CloudVersion: 0, LocalConfig: safe, LocalPayload: encrypted, CloudConfig: emptyJSON(), DiffFields: json.RawMessage(`[]`)}
			if e = tx.Table(domainConfigsTable).Create(&record).Error; e != nil {
				return e
			}
		} else {
			if e = tx.Table(domainConfigsTable).Where("id=?", current.ID).Updates(map[string]any{"local_version": version, "local_config": safe, "local_payload": encrypted}).Error; e != nil {
				return e
			}
		}
		id := snowflake.GenID()
		task = &ConfigTaskRecord{ID: id, TaskNo: fmt.Sprintf("CC%d", id), DomainID: domainID, ConfigModule: module, Version: version, Payload: encrypted, Status: "pending", MaxAttempts: 5, OperatorType: operator, OperatorID: userID, CreatedAt: s.now()}
		return tx.Table(configTasksTable).Create(task).Error
	})
	if err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Service) ConfigTasks(ctx context.Context, userID, domainID int64, admin bool) ([]ConfigTaskRecord, error) {
	domain, err := s.domain(ctx, domainID)
	if err != nil {
		return nil, err
	}
	if !admin && domain.UserID != userID {
		return nil, ErrForbidden
	}
	var rows []ConfigTaskRecord
	err = s.db.WithContext(ctx).Table(configTasksTable).Select("id,task_no,domain_id,config_module,version,status,error,attempts,max_attempts,request_id,operator_type,operator_id,created_at,finished_at").Where("domain_id=?", domainID).Order("id DESC").Limit(50).Find(&rows).Error
	return rows, err
}

func normalizeKey(value string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(value))
}
func flattenConfig(prefix string, value any, out map[string]any) {
	switch item := value.(type) {
	case map[string]any:
		for key, val := range item {
			next := key
			if prefix != "" {
				next = prefix + "." + key
			}
			flattenConfig(next, val, out)
		}
	case []any:
		out[normalizeKey(prefix)] = item
	default:
		out[normalizeKey(prefix)] = item
	}
}
func configDiff(local, cloud map[string]any) []string {
	localFlat, cloudFlat := map[string]any{}, map[string]any{}
	flattenConfig("", local, localFlat)
	flattenConfig("", cloud, cloudFlat)
	fields := make([]string, 0)
	for key, expected := range localFlat {
		actual, ok := cloudFlat[key]
		if !ok || !reflect.DeepEqual(expected, actual) {
			fields = append(fields, key)
		}
	}
	sort.Strings(fields)
	return fields
}

// The cloud appends a read-only global cache default. Verify all writable rules
// in their returned order, while keeping the unfiltered readback for audit.
func moduleConfigDiff(module string, local, cloud map[string]any) []string {
	if module == "cache_rules" {
		if rules, ok := cloud["rules"].([]any); ok {
			filtered := []any{}
			for _, raw := range rules {
				row, ok := raw.(map[string]any)
				if ok && row["type"] == "global" {
					continue
				}
				filtered = append(filtered, raw)
			}
			copy := map[string]any{}
			for key, value := range cloud {
				copy[key] = value
			}
			copy["rules"] = filtered
			return configDiff(local, copy)
		}
	}
	return configDiff(local, cloud)
}

func (s *Service) syncDomainConfig(ctx context.Context, domain DomainRecord, module string, version int64) error {
	_, account, err := s.accountSecret(ctx, domain.AccountID)
	if err != nil {
		return err
	}
	if err = s.waitCloudGap(ctx); err != nil {
		return err
	}
	cloud, err := s.cloud.GetDomainConfig(ctx, account, domain.ServiceNo, module)
	if err != nil {
		return err
	}
	// Compare the actual cloud values first; persist only redacted content.
	var record DomainConfigRecord
	if err = s.db.WithContext(ctx).Table(domainConfigsTable).Where("domain_id=? AND config_module=?", domain.ID, module).Take(&record).Error; err != nil {
		return err
	}
	if version != 0 && version != record.LocalVersion {
		return errors.New("已有更新配置，旧版本未确认")
	}
	payload := record.LocalPayload
	if payload == "" {
		var task ConfigTaskRecord
		if e := s.db.WithContext(ctx).Table(configTasksTable).Where("domain_id=? AND config_module=? AND version=?", domain.ID, module, record.LocalVersion).Order("id DESC").Take(&task).Error; e != nil {
			return errors.New("配置核验载荷不可用，请重新保存配置")
		}
		payload = task.Payload
	}
	plain, e := cryptoutil.Decrypt(payload, s.key)
	if e != nil {
		return errors.New("配置核验载荷不可用")
	}
	var local map[string]any
	if json.Unmarshal([]byte(plain), &local) != nil {
		return errors.New("配置核验载荷无效")
	}
	plain = ""
	diff := moduleConfigDiff(module, local, cloud)
	cloudJSON, _ := json.Marshal(sanitizeConfig(cloud))
	diffJSON, _ := json.Marshal(diff)
	if version == 0 {
		version = record.LocalVersion
	}
	updates := map[string]any{"cloud_config": cloudJSON, "diff_fields": diffJSON}
	if len(diff) == 0 {
		updates["cloud_version"] = version
	}
	update := s.db.WithContext(ctx).Table(domainConfigsTable).Where("id=? AND local_version=?", record.ID, record.LocalVersion).Updates(updates)
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected == 0 {
		var count int64
		if e := s.db.WithContext(ctx).Table(domainConfigsTable).Where("id=? AND local_version=?", record.ID, record.LocalVersion).Count(&count).Error; e != nil {
			return e
		}
		if count != 1 {
			return errors.New("配置版本已更新，请重新核验")
		}
	}
	if len(diff) > 0 {
		return errors.New("配置回读存在差异或缺失字段，请核查后同步，未标记成功")
	}
	return nil
}
