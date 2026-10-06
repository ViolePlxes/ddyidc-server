package instance

import (
	"context"
	dom "ddyidc/internal/instance/domain"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/upstream/provider/contracts"
	"ddyidc/pkg/outbound"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type SecurityGroupRecord struct {
	ID                                                    int64 `gorm:"primaryKey"`
	InstanceID, CustomerID, OwnershipVersion, SupplierID  int64
	HostID, RemoteID, Name, Description, RequestID, State string
	CreatedAt, UpdatedAt                                  time.Time
}

func (SecurityGroupRecord) TableName() string { return "instance.instance_security_groups" }

type SecurityInput struct {
	Action      string                 `json:"action"`
	GroupID     string                 `json:"groupId"`
	RequestID   string                 `json:"requestId"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Rule        contracts.SecurityRule `json:"rule"`
}
type SecurityView struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Applied     bool                     `json:"applied"`
	State       string                   `json:"state"`
	Rules       []contracts.SecurityRule `json:"rules"`
}

func validSecurityRule(r contracts.SecurityRule) bool {
	if r.Direction != "in" && r.Direction != "out" {
		return false
	}
	if r.Protocol != "tcp" && r.Protocol != "udp" && r.Protocol != "icmp" && r.Protocol != "all" && r.Protocol != "all_tcp" && r.Protocol != "all_udp" && r.Protocol != "gre" {
		return false
	}
	if _, e := netip.ParsePrefix(r.IP); e != nil {
		if _, e = netip.ParseAddr(r.IP); e != nil {
			return false
		}
	}
	if len(r.Port) > 128 || r.Port == "" {
		return false
	}
	for _, piece := range strings.Split(r.Port, ",") {
		ends := strings.Split(piece, "-")
		if len(ends) > 2 {
			return false
		}
		lo, e := strconv.Atoi(ends[0])
		if e != nil || lo < 1 || lo > 65535 {
			return false
		}
		if len(ends) == 2 {
			hi, e := strconv.Atoi(ends[1])
			if e != nil || hi < lo || hi > 65535 {
				return false
			}
		}
	}
	return true
}
func (s *Service) SecurityGroups(ctx context.Context, customer, id int64) ([]SecurityView, error) {
	if s.db == nil {
		return nil, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	h, e := s.owned(ctx, customer, id)
	if e != nil {
		return nil, e
	}
	p, w := s.providerContext(ctx, h)
	if w != "" {
		return nil, ErrUnsupported
	}
	sp, ok := p.(contracts.SecurityProvider)
	if !ok {
		return nil, ErrUnsupported
	}
	remote, e := sp.SecurityGroups(parseHostID(h.UpstreamInstanceID))
	if e != nil {
		return nil, e
	}
	var rows []SecurityGroupRecord
	if e = s.db.WithContext(ctx).Where("instance_id=? AND customer_id=? AND ownership_version=? AND supplier_id=? AND host_id=? AND state<>'deleted'", id, customer, h.OwnershipVersion, h.UpstreamProviderID, h.UpstreamInstanceID).Find(&rows).Error; e != nil {
		return nil, e
	}
	out := []SecurityView{}
	for _, row := range rows {
		if row.State == "unknown" {
			var commands []struct {
				RequestID string
				Payload   json.RawMessage
			}
			if e = s.db.WithContext(ctx).Table("instance.instance_security_commands").Select("request_id,payload").Where("instance_id=? AND customer_id=? AND state='unknown'", id, customer).Find(&commands).Error; e != nil {
				return nil, e
			}
			for _, command := range commands {
				var input SecurityInput
				if json.Unmarshal(command.Payload, &input) != nil {
					continue
				}
				if input.Action == "create" && command.RequestID != row.RequestID || input.Action != "create" && input.GroupID != fmt.Sprint(row.ID) {
					continue
				}
				var group *contracts.SecurityGroup
				matches := 0
				for index, g := range remote {
					if g.Name == fmt.Sprintf("ddy-%d", row.ID) && (row.RemoteID == "" || g.ID == row.RemoteID) {
						group = &remote[index]
						matches++
					}
				}
				if matches > 1 {
					return nil, errors.New("安全组标识冲突，需人工核查")
				}
				state, remoteID := "", row.RemoteID
				if group != nil {
					remoteID = group.ID
				}
				switch input.Action {
				case "create":
					if group != nil {
						state = "active"
					}
				case "delete":
					if group == nil && remoteID != "" {
						state = "deleted"
					}
				case "apply":
					if group != nil && group.Applied {
						state = "active"
					}
				case "add_rule", "delete_rule":
					if group != nil {
						rules, err := sp.SecurityRules(parseHostID(h.UpstreamInstanceID), group.ID)
						if err != nil {
							return nil, err
						}
						found := false
						for _, rule := range rules {
							if input.Action == "delete_rule" && rule.ID == input.Rule.ID || input.Action == "add_rule" && rule.Direction == input.Rule.Direction && rule.Protocol == input.Rule.Protocol && rule.Port == input.Rule.Port && rule.IP == input.Rule.IP {
								found = true
							}
						}
						if input.Action == "add_rule" && found || input.Action == "delete_rule" && !found {
							state = "active"
						}
					}
				}
				if state != "" {
					if e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
						if e := tx.Model(&SecurityGroupRecord{}).Where("id=? AND customer_id=? AND ownership_version=? AND state='unknown'", row.ID, customer, h.OwnershipVersion).Updates(map[string]any{"state": state, "remote_id": remoteID, "updated_at": time.Now()}).Error; e != nil {
							return e
						}
						return tx.Table("instance.instance_security_commands").Where("request_id=? AND state='unknown'", command.RequestID).Update("state", "completed").Error
					}); e != nil {
						return nil, e
					}
					row.State, row.RemoteID = state, remoteID
				}
			}
		}
		if row.State == "deleted" {
			continue
		}
		matched := false
		v := SecurityView{ID: strconv.FormatInt(row.ID, 10), Name: row.Name, Description: row.Description, State: row.State, Rules: []contracts.SecurityRule{}}
		for _, g := range remote {
			if g.ID == row.RemoteID && g.Name == fmt.Sprintf("ddy-%d", row.ID) {
				matched = true
				v.Applied = g.Applied
				if row.State == "active" {
					v.Rules, e = sp.SecurityRules(parseHostID(h.UpstreamInstanceID), g.ID)
					if e != nil {
						return nil, e
					}
				}
			}
		}
		if !matched && v.State == "active" {
			v.State = "unknown"
		}
		out = append(out, v)
	}
	return out, nil
}
func (s *Service) SecurityOperation(ctx context.Context, customer, id int64, in SecurityInput) error {
	if s.db == nil || outbound.DetailReadOnly() {
		return ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if len(in.RequestID) < 8 || len(in.RequestID) > 64 {
		return errors.New("缺少安全组操作请求编号")
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Action == "create" {
		if len(in.RequestID) < 8 || len(in.RequestID) > 64 || strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Name) > 80 || utf8.RuneCountInString(in.Description) > 255 {
			return errors.New("安全组名称或请求参数无效")
		}
	} else if in.Action != "apply" && in.Action != "delete" && in.Action != "add_rule" && in.Action != "delete_rule" {
		return ErrUnsupported
	}
	if in.Action == "add_rule" && !validSecurityRule(in.Rule) {
		return errors.New("安全组规则方向、协议、端口或IP格式无效")
	}
	h, e := s.owned(ctx, customer, id)
	if e != nil {
		return e
	}
	if h.UpstreamProviderID == nil || h.Status == 6 {
		return ErrUnsupported
	}
	locked, e := s.repo.IsMarketplaceLocked(ctx, id)
	if e != nil {
		return e
	}
	if locked {
		return ErrTradingLocked
	}
	p, w := s.providerContext(ctx, h)
	if w != "" {
		return ErrUnsupported
	}
	sp, ok := p.(contracts.SecurityProvider)
	if !ok {
		return ErrUnsupported
	}
	real, e := p.GetInstanceDetail(parseHostID(h.UpstreamInstanceID))
	if e != nil || real == nil || real.ID != h.UpstreamInstanceID {
		return errors.New("实例信息未确认，请刷新后重试")
	}
	if unavailableLifecycle(real, h.UpstreamInstanceID) != "" {
		return ErrUnsupported
	}
	var row SecurityGroupRecord
	// A durable reservation precedes every remote write. Unknown outcomes cannot
	// be replayed by refreshing the browser or reusing a request ID.
	e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current dom.Host
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND ownership_version=? AND upstream_instance_id=? AND status<>6", id, customer, h.OwnershipVersion, h.UpstreamInstanceID).Take(&current).Error; e != nil {
			return ErrForbidden
		}
		if e := s.refundMutationAllowed(ctx, tx, id); e != nil {
			return e
		}
		if e := s.upgradeMutationAllowed(ctx, tx, id); e != nil {
			return e
		}
		if e := s.refundHostUnlocked(tx, id); e != nil {
			return e
		}
		var trading int64
		if e := tx.Table("system.marketplace_listings").Where("product_id=? AND status IN ('pending','on_sale','locked')", id).Count(&trading).Error; e != nil {
			return e
		}
		if trading > 0 {
			return ErrTradingLocked
		}
		payload, e := json.Marshal(in)
		if e != nil {
			return e
		}
		if e = tx.Exec("INSERT INTO instance.instance_security_commands(request_id,instance_id,customer_id,action,payload,state,created_at) VALUES(?,?,?,?,?,'unknown',NOW(3))", in.RequestID, id, customer, in.Action, string(payload)).Error; e != nil {
			return errors.New("操作请求已处理，请刷新查看结果")
		}
		if in.Action == "create" {
			var n int64
			if e := tx.Model(&SecurityGroupRecord{}).Where("instance_id=? AND state NOT IN ('deleted','failed')", id).Count(&n).Error; e != nil {
				return e
			}
			if n >= 20 {
				return errors.New("每台实例最多创建20个安全组")
			}
			if e := tx.Model(&SecurityGroupRecord{}).Where("request_id=?", in.RequestID).Count(&n).Error; e != nil {
				return e
			}
			if n != 0 {
				return errors.New("请求已处理，请刷新安全组列表")
			}
			row = SecurityGroupRecord{ID: snowflake.GenID(), InstanceID: id, CustomerID: customer, OwnershipVersion: h.OwnershipVersion, SupplierID: *h.UpstreamProviderID, HostID: h.UpstreamInstanceID, Name: in.Name, Description: in.Description, RequestID: in.RequestID, State: "unknown", CreatedAt: time.Now(), UpdatedAt: time.Now()}
			return tx.Create(&row).Error
		}
		gid, e := strconv.ParseInt(in.GroupID, 10, 64)
		if e != nil || gid <= 0 {
			return ErrNotFound
		}
		if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND instance_id=? AND customer_id=? AND ownership_version=? AND supplier_id=? AND host_id=? AND state='active'", gid, id, customer, h.OwnershipVersion, h.UpstreamProviderID, h.UpstreamInstanceID).Take(&row).Error; e != nil {
			return ErrForbidden
		}
		return tx.Model(&row).Updates(map[string]any{"state": "unknown", "updated_at": time.Now()}).Error
	})
	if e != nil {
		return e
	}
	sent := false
	defer func() {
		if !sent {
			state := "active"
			if in.Action == "create" {
				state = "failed"
			}
			_ = s.db.WithContext(context.WithoutCancel(ctx)).Model(&SecurityGroupRecord{}).Where("id=? AND state='unknown'", row.ID).Updates(map[string]any{"state": state, "updated_at": time.Now()}).Error
			_ = s.db.WithContext(context.WithoutCancel(ctx)).Exec("UPDATE instance.instance_security_commands SET state='failed' WHERE request_id=? AND instance_id=? AND customer_id=? AND state='unknown'", in.RequestID, id, customer).Error
		}
	}()
	// Hold the ownership row lock until the bounded remote operation finishes.
	// Transfer and trading workflows use this same row, so ownership cannot change
	// between the final permission check and the remote mutation.
	return s.db.WithContext(ctx).Transaction(func(lockTx *gorm.DB) error {
		var current dom.Host
		if e := lockTx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND customer_id=? AND ownership_version=? AND upstream_provider_id=? AND upstream_instance_id=? AND status<>6", id, customer, h.OwnershipVersion, h.UpstreamProviderID, h.UpstreamInstanceID).Take(&current).Error; e != nil {
			return ErrForbidden
		}
		if e := s.refundMutationAllowed(ctx, lockTx, id); e != nil {
			return e
		}
		if e := s.upgradeMutationAllowed(ctx, lockTx, id); e != nil {
			return e
		}
		if e := s.refundHostUnlocked(lockTx, id); e != nil {
			return e
		}
		command := "createSecurityGroup"
		fields := map[string]string{"name": fmt.Sprintf("ddy-%d", row.ID), "description": row.Description}
		if in.Action != "create" {
			groups, e := sp.SecurityGroups(parseHostID(h.UpstreamInstanceID))
			if e != nil {
				return e
			}
			found := false
			for _, g := range groups {
				if g.ID == row.RemoteID && g.Name == fmt.Sprintf("ddy-%d", row.ID) {
					found = true
					if in.Action == "delete" && g.Applied {
						return errors.New("应用中的安全组不能删除")
					}
				}
			}
			if !found {
				return ErrForbidden
			}
			fields = map[string]string{"id": row.RemoteID}
			switch in.Action {
			case "apply":
				command = "linkSecurityGroup"
			case "delete":
				command = "delSecurityGroup"
			case "add_rule":
				command = "createSecurityRule"
				fields["direction"] = in.Rule.Direction
				fields["protocol"] = in.Rule.Protocol
				fields["port"] = in.Rule.Port
				fields["ip"] = in.Rule.IP
			case "delete_rule":
				command = "delSecurityRule"
				rules, e := sp.SecurityRules(parseHostID(h.UpstreamInstanceID), row.RemoteID)
				if e != nil {
					return e
				}
				found = false
				for _, r := range rules {
					if r.ID == in.Rule.ID && r.ID != "" {
						found = true
					}
				}
				if !found {
					return ErrForbidden
				}
				fields["id"] = in.Rule.ID
				fields["group"] = row.RemoteID
			}
		}
		actor, operator := actionActor(ctx, customer)
		event := &dom.Event{InstanceID: id, EventType: "security_group", OperatorType: actor, OperatorID: &operator, Status: 1, UpstreamRequest: fmt.Sprintf(`{"action":%q,"groupId":%q}`, in.Action, strconv.FormatInt(row.ID, 10)), UpstreamResponse: "{}"}
		if e = s.repo.CreateEvent(ctx, event); e != nil {
			return e
		}
		sent = true
		if e = sp.SecurityCommand(parseHostID(h.UpstreamInstanceID), command, fields); e != nil {
			_ = s.repo.FinishEvent(ctx, event.ID, 3, nil, "", "安全组操作结果待核对")
			return e
		}
		remoteID := row.RemoteID
		if in.Action == "create" {
			groups, e := sp.SecurityGroups(parseHostID(h.UpstreamInstanceID))
			if e != nil {
				return e
			}
			for _, g := range groups {
				if g.Name == fields["name"] {
					if remoteID != "" {
						return errors.New("安全组标识冲突")
					}
					remoteID = g.ID
				}
			}
			if remoteID == "" {
				return errors.New("安全组已提交，标识待核对")
			}
		}
		state := "active"
		if in.Action == "delete" {
			state = "deleted"
		}
		e = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if e := tx.Model(&SecurityGroupRecord{}).Where("id=? AND state='unknown'", row.ID).Updates(map[string]any{"state": state, "remote_id": remoteID, "updated_at": time.Now()}).Error; e != nil {
				return e
			}
			return tx.Exec("UPDATE instance.instance_security_commands SET state='completed' WHERE request_id=? AND instance_id=? AND customer_id=?", in.RequestID, id, customer).Error
		})
		if e != nil {
			return e
		}
		return s.repo.FinishEvent(ctx, event.ID, 2, nil, `{"success":true}`, "")
	})
}
