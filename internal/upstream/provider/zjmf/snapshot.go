package zjmf

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"ddyidc/internal/upstream/provider/contracts"
)

var snapshotDiskPattern = regexp.MustCompile(`<option value="([0-9]+)"[^>]*>([^<]+)</option>`)

func parseClientAreas(raw any) []contracts.InstanceClientArea {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	areas := make([]contracts.InstanceClientArea, 0, len(items))
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := row["key"].(string)
		name, _ := row["name"].(string)
		key, name = strings.TrimSpace(key), strings.TrimSpace(name)
		if key == "" {
			continue
		}
		areas = append(areas, contracts.InstanceClientArea{Key: key, Name: name})
	}
	return areas
}

func (p *Provider) snapshotContent(hostID int64) (string, error) {
	body, err := p.authorizedFormPost("/zjmf_api/provision/custom/content", url.Values{
		"id": {fmt.Sprint(hostID)}, "key": {"snapshot"},
	})
	if err != nil {
		return "", err
	}
	var payload struct {
		Status int `json:"status"`
		Data   struct {
			HTML string `json:"html"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Status != 200 {
		return "", errors.New("快照信息读取失败")
	}
	return payload.Data.HTML, nil
}

func (p *Provider) ListSnapshots(hostID int64) ([]contracts.Snapshot, error) {
	if hostID <= 0 {
		return nil, errors.New("upstream host ID must be positive")
	}
	body, err := p.authorizedFormPost(fmt.Sprintf("/provision/custom/%d", hostID), url.Values{"func": {"listSnapBackup"}})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Status int `json:"status"`
		Data   []struct {
			ID         int64  `json:"id"`
			Type       string `json:"type"`
			DiskName   string `json:"disk_name"`
			Remarks    string `json:"remarks"`
			CreateTime string `json:"create_time"`
			Status     int    `json:"status"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Status != 200 {
		return nil, errors.New("快照列表读取失败")
	}
	out := make([]contracts.Snapshot, 0, len(payload.Data))
	for _, item := range payload.Data {
		out = append(out, contracts.Snapshot{
			ID: fmt.Sprint(item.ID), Type: item.Type, DiskName: item.DiskName,
			Remarks: item.Remarks, CreateTime: item.CreateTime, Status: item.Status,
		})
	}
	return out, nil
}

func (p *Provider) CreateSnapshot(hostID int64, name string) error {
	if hostID <= 0 || strings.TrimSpace(name) == "" {
		return errors.New("请填写快照备注名")
	}
	html, err := p.snapshotContent(hostID)
	if err != nil {
		return err
	}
	match := snapshotDiskPattern.FindStringSubmatch(html)
	if len(match) != 3 {
		return errors.New("未找到可创建快照的磁盘")
	}
	return p.snapshotAction(hostID, url.Values{"func": {"createSnap"}, "id": {match[1]}, "name": {name}})
}

func (p *Provider) RestoreSnapshot(hostID int64, snapshotID string) error {
	if strings.TrimSpace(snapshotID) == "" {
		return errors.New("快照编号无效")
	}
	return p.snapshotAction(hostID, url.Values{"func": {"restoreSnap"}, "id": {snapshotID}})
}

func (p *Provider) DeleteSnapshot(hostID int64, snapshotID string) error {
	if strings.TrimSpace(snapshotID) == "" {
		return errors.New("快照编号无效")
	}
	return p.snapshotAction(hostID, url.Values{"func": {"deleteSnap"}, "id": {snapshotID}})
}

func (p *Provider) snapshotAction(hostID int64, form url.Values) error {
	body, err := p.authorizedFormPost(fmt.Sprintf("/provision/custom/%d", hostID), form)
	if err != nil {
		return err
	}
	var payload struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return errors.New("快照操作返回格式无效")
	}
	if payload.Status != 200 {
		if payload.Msg == "" {
			payload.Msg = "快照操作失败"
		}
		return errors.New(payload.Msg)
	}
	return nil
}
