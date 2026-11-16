package west

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ddyidc/internal/upstream/provider/contracts"
)

// WithInstanceContext bounds reads and actions with the caller's deadline. The
// copy shares credentials and pacing with the parent provider.
func (p *Provider) WithInstanceContext(ctx context.Context) contracts.InstanceProvider {
	scoped := *p
	scoped.instanceContext = ctx
	return &scoped
}

var capabilities = contracts.InstanceCapabilities{
	Start:         true,
	Stop:          true,
	Reboot:        true,
	Reinstall:     true,
	ResetPassword: true,
	// No documented terminate, rescue, hard power or VNC-session protocol.
	HardStop:   false,
	HardReboot: false,
	Rescue:     false,
	ExitRescue: false,
	Traffic:    false,
	Remote:     false,
	NAT:        false,
	Destroy:    false,
}

// powerOf maps the run-state codes to the platform vocabulary. The vendor table
// documents 0/1/2/3; a freshly created machine answers -1 ("creating"), which was
// observed on the first live trial machine and is treated as provisioning.
func powerOf(state *runState) string {
	switch int(dec(state.State)) {
	case -1:
		return "provisioning"
	case 0:
		return "running"
	case 1, 2:
		return "stopped"
	case 3:
		return "suspended"
	default:
		return ""
	}
}

var timeLayouts = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02", "2006/01/02 15:04:05", "2006/01/02"}

func parseWestTime(raw string) *time.Time {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	for _, layout := range timeLayouts {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return &parsed
		}
	}
	return nil
}

func firstText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// formatMemory converts the vendor's megabyte figure into the GB figure the
// order page sells, keeping half gigabytes readable ("0.5 G").
func formatMemory(raw string) string {
	mb, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || mb <= 0 {
		return strings.TrimSpace(raw)
	}
	if mb%1024 == 0 {
		return fmt.Sprintf("%d G", mb/1024)
	}
	return fmt.Sprintf("%.1f G", float64(mb)/1024)
}

func (p *Provider) GetInstanceDetail(hostID int64) (*contracts.InstanceDetail, error) {
	if hostID <= 0 {
		return nil, errors.New("上游实例编号无效")
	}
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return nil, err
	}
	info, err := p.serverInfo(ctx, ip)
	if err != nil {
		return nil, err
	}
	// A mismatched identity means the answer belongs to another account or host.
	if id := dec(info.ID); id != 0 && int64(id) != hostID {
		return nil, errors.New("西部数码返回的实例身份不匹配")
	}
	state := ""
	if run, err := p.runState(ctx, ip); err == nil {
		state = powerOf(run)
	}
	detail := &contracts.InstanceDetail{
		ID:              strconv.FormatInt(hostID, 10),
		PrimaryIP:       ip,
		SubnetMask:      strings.TrimSpace(info.Mask),
		Gateway:         strings.TrimSpace(info.Gateway),
		OperatingSystem: firstText(info.OSText, info.OS),
		Port:            strings.TrimSpace(info.Port),
		PortIsDefault:   true,
		Username:        strings.TrimSpace(info.Login),
		Status:          state,
		PowerStatus:     state,
		Capabilities:    capabilities,
		ExpiresAt:       parseWestTime(info.Expires),
		ActivatedAt:     parseWestTime(info.StartTime),
		// Empty collections must serialise as [] so the instance pages never call
		// .find on null.
		Config:      []contracts.InstanceConfig{},
		IPs:         []contracts.InstanceIP{},
		ClientAreas: []contracts.InstanceClientArea{},
	}
	if room, err := strconv.Atoi(strings.TrimSpace(info.Room)); err == nil && room > 0 {
		detail.ProductID = strconv.Itoa(room)
	}
	detail.IPs = []contracts.InstanceIP{{Address: ip, SubnetMask: detail.SubnetMask, Gateway: detail.Gateway}}
	for _, extra := range info.ExtraIPs {
		if extra = strings.TrimSpace(extra); extra != "" && extra != ip {
			detail.IPs = append(detail.IPs, contracts.InstanceIP{Address: extra})
		}
	}
	addConfig := func(field, name, value string) {
		if value = strings.TrimSpace(value); value != "" {
			detail.Config = append(detail.Config, contracts.InstanceConfig{ID: field, Field: field, Name: name, Value: value})
		}
	}
	addConfig("cpu", "CPU", info.CPU)
	addConfig("memory", "内存", formatMemory(info.Memory))
	addConfig("osdisk", "系统盘", info.SystemDisk)
	addConfig("harddisk", "总硬盘", info.TotalDisk)
	addConfig("flux", "带宽(M)", text(info.Bandwidth))
	addConfig("disktype", "云盘类型", info.DiskType)
	addConfig("room", "机房", info.Room)
	addConfig("ebs", "可用区", info.ZoneName)
	addConfig("ddos", "高防值", text(info.DDoS))
	return detail, nil
}

func (p *Provider) GetInstanceStatus(hostID int64) (string, error) {
	if hostID <= 0 {
		return "", errors.New("上游实例编号无效")
	}
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return "", err
	}
	state, err := p.runState(ctx, ip)
	if err != nil {
		return "", err
	}
	power := powerOf(state)
	if power == "" {
		return "", errors.New("西部数码返回了未知的实例状态")
	}
	return power, nil
}

func (p *Provider) ExecuteInstanceAction(hostID int64, input contracts.InstanceActionInput) (*contracts.InstanceActionResult, error) {
	if hostID <= 0 {
		return nil, errors.New("上游实例编号无效")
	}
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return nil, err
	}
	switch input.Action {
	case "start":
		return actionResult("starting", p.setState(ctx, ip, "on"))
	case "stop":
		return actionResult("stopping", p.setState(ctx, ip, "off"))
	case "reboot":
		return actionResult("rebooting", p.setState(ctx, ip, "reboot"))
	case "reset_password":
		if len(input.Password) < 12 {
			return nil, errors.New("新密码至少 12 位")
		}
		return actionResult("running", p.changePassword(ctx, ip, input.Password))
	case "reinstall":
		osValue, err := p.osValue(ctx, input.ImageID)
		if err != nil {
			return nil, err
		}
		return actionResult("reinstalling", p.reinstall(ctx, ip, osValue, !input.FormatDataDisk))
	default:
		// Hard power, rescue, terminate and VNC have no documented protocol.
		return nil, errors.New("西部数码实例暂不支持该操作")
	}
}

func actionResult(status string, err error) (*contracts.InstanceActionResult, error) {
	if err != nil {
		return nil, err
	}
	return &contracts.InstanceActionResult{Status: status, Message: "操作已提交，状态将在稍后同步"}, nil
}

// osValue resolves the platform image identity back to the upstream value that
// create and reinstall expect.
func (p *Provider) osValue(ctx context.Context, imageID string) (string, error) {
	id, err := strconv.Atoi(strings.TrimSpace(imageID))
	if err != nil || id <= 0 {
		return "", errors.New("请选择要安装的操作系统")
	}
	cfg, err := p.config(ctx)
	if err != nil {
		return "", err
	}
	list := imageList(cfg)
	if id > len(list) {
		return "", errors.New("所选操作系统不在西部数码当前列表中")
	}
	value := strings.TrimSpace(list[id-1].Value)
	if value == "" {
		return "", errors.New("所选操作系统缺少上游标识")
	}
	return value, nil
}

func (p *Provider) GetInstanceMonitor(hostID int64, start, end time.Time) (*contracts.InstanceMonitor, error) {
	if hostID <= 0 {
		return nil, errors.New("上游实例编号无效")
	}
	ctx, cancel := p.withTimeout()
	defer cancel()
	ip, err := p.ipOf(ctx, hostID)
	if err != nil {
		return nil, err
	}
	charts, err := p.charts(ctx, ip)
	if err != nil {
		return nil, err
	}
	hour := charts.Hour
	if len(hour.CPU) == 0 && len(hour.NetIn) == 0 {
		return &contracts.InstanceMonitor{Available: false, Message: "上游暂无可用的性能数据"}, nil
	}
	span := end.Sub(start)
	if span <= 0 {
		span = 24 * time.Hour
	}
	// The upstream series is hourly and carries no absolute dates; lay the points
	// evenly across the requested window so the chart keeps a stable order.
	points := func(series []json.Number) []contracts.MonitorPoint {
		out := make([]contracts.MonitorPoint, 0, len(series))
		for i, raw := range series {
			at := start
			if len(series) > 1 {
				at = start.Add(time.Duration(int64(span) * int64(i) / int64(len(series)-1)))
			}
			out = append(out, contracts.MonitorPoint{Timestamp: at.Unix(), Value: dec(raw)})
		}
		return out
	}
	return &contracts.InstanceMonitor{
		CPU:        points(hour.CPU),
		DiskRead:   points(hour.DiskRead),
		DiskWrite:  points(hour.DiskWrite),
		NetworkIn:  points(hour.NetIn),
		NetworkOut: points(hour.NetOut),
		Available:  true,
	}, nil
}
