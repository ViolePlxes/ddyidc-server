package product

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	domain "ddyidc/internal/product/domain"
	repository "ddyidc/internal/product/repository"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

type groupInput struct {
	Name        string `json:"name"`
	ParentID    string `json:"parentId"`
	Status      *uint8 `json:"status"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

func decodeAdminJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(w, http.StatusBadRequest, "请求参数格式错误")
		return false
	}
	return true
}

func parseID(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("ID 无效")
	}
	return value, nil
}

func parseOptionalID(raw string) (int64, error) {
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "0" {
		return 0, nil
	}
	return parseID(raw)
}

func groupPathID(r *http.Request) (int64, error) {
	return parseID(pathvar.Vars(r)["id"])
}

func parseIDs(values []string) ([]int64, error) {
	ids := make([]int64, 0, len(values))
	for _, raw := range values {
		id, err := parseID(raw)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func isAdminInputError(err error) bool {
	return errors.Is(err, repository.ErrGroupNotFound) ||
		errors.Is(err, repository.ErrGroupMove) ||
		errors.Is(err, repository.ErrGroupLevel) ||
		errors.Is(err, repository.ErrGroupHasChildren) ||
		errors.Is(err, repository.ErrGroupHasProducts) ||
		errors.Is(err, repository.ErrInvalidSort) ||
		errors.Is(err, repository.ErrInvalidProducts) ||
		strings.Contains(err.Error(), "最多支持两级")
}

func writeAdminMutation(w http.ResponseWriter, data any, err error, failure string) {
	if err == nil {
		writeJSON(w, http.StatusOK, data)
		return
	}
	if isAdminInputError(err) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, failure)
}

func (h *publicHandler) adminGroups(w http.ResponseWriter, r *http.Request) {
	parentID, err := parseOptionalID(r.URL.Query().Get("parentId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "父分组 ID 无效")
		return
	}
	items, err := h.products.ListAdminGroups(r.Context(), parentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取商品分组失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"list": items, "count": len(items)})
}

func validateGroupInput(w http.ResponseWriter, input *groupInput) (int64, uint8, bool) {
	input.Name = strings.TrimSpace(input.Name)
	input.Icon = strings.TrimSpace(input.Icon)
	if input.Name == "" || len([]rune(input.Name)) > 64 {
		writeError(w, http.StatusBadRequest, "分组名称不能为空且最多 64 个字符")
		return 0, 0, false
	}
	if len([]rune(input.Icon)) > 255 {
		writeError(w, http.StatusBadRequest, "分组图标最多 255 个字符")
		return 0, 0, false
	}
	if strings.TrimSpace(input.Description) != "" {
		writeError(w, http.StatusBadRequest, "当前商品分组表不支持描述字段")
		return 0, 0, false
	}
	parentID, err := parseOptionalID(input.ParentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "父分组 ID 无效")
		return 0, 0, false
	}
	status := uint8(1)
	if input.Status != nil {
		status = *input.Status
	}
	if status > 1 {
		writeError(w, http.StatusBadRequest, "分组状态无效")
		return 0, 0, false
	}
	return parentID, status, true
}

func (h *publicHandler) createGroup(w http.ResponseWriter, r *http.Request) {
	var input groupInput
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	parentID, status, ok := validateGroupInput(w, &input)
	if !ok {
		return
	}
	group := &domain.ProductGroup{ParentID: parentID, Name: input.Name, Icon: input.Icon, Status: status}
	err := h.products.CreateAdminGroup(r.Context(), group)
	writeAdminMutation(w, group, err, "创建商品分组失败")
}

func (h *publicHandler) updateGroup(w http.ResponseWriter, r *http.Request) {
	id, err := groupPathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "商品分组 ID 无效")
		return
	}
	var input groupInput
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	_, status, ok := validateGroupInput(w, &input)
	if !ok {
		return
	}
	err = h.products.UpdateAdminGroup(r.Context(), id, input.Name, input.Icon, status)
	writeAdminMutation(w, map[string]bool{"updated": true}, err, "更新商品分组失败")
}

func (h *publicHandler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id, err := groupPathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "商品分组 ID 无效")
		return
	}
	err = h.products.DeleteAdminGroup(r.Context(), id)
	writeAdminMutation(w, map[string]bool{"deleted": true}, err, "删除商品分组失败")
}

func (h *publicHandler) setGroupHidden(w http.ResponseWriter, r *http.Request) {
	id, err := groupPathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "商品分组 ID 无效")
		return
	}
	var input struct {
		Hidden int `json:"hidden"`
	}
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	if input.Hidden != 0 && input.Hidden != 1 {
		writeError(w, http.StatusBadRequest, "隐藏状态无效")
		return
	}
	err = h.products.SetAdminGroupHidden(r.Context(), id, input.Hidden == 1)
	writeAdminMutation(w, map[string]int{"hidden": input.Hidden}, err, "更新商品分组状态失败")
}

func (h *publicHandler) sortGroups(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ParentID string   `json:"parentId"`
		IDs      []string `json:"ids"`
	}
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	parentID, err := parseOptionalID(input.ParentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "父分组 ID 无效")
		return
	}
	ids, err := parseIDs(input.IDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "排序分组 ID 无效")
		return
	}
	err = h.products.SortAdminGroups(r.Context(), parentID, ids)
	writeAdminMutation(w, map[string]bool{"sorted": true}, err, "保存分组排序失败")
}

func (h *publicHandler) moveProducts(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs           []string `json:"ids"`
		TargetGroupID string   `json:"targetGroupId"`
	}
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	ids, err := parseIDs(input.IDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "商品 ID 无效")
		return
	}
	targetID, err := parseID(input.TargetGroupID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "目标分组 ID 无效")
		return
	}
	err = h.products.MoveProducts(r.Context(), ids, targetID)
	writeAdminMutation(w, map[string]bool{"moved": true}, err, "移动商品失败")
}

func (h *publicHandler) sortProducts(w http.ResponseWriter, r *http.Request) {
	var input struct {
		GroupID string   `json:"groupId"`
		IDs     []string `json:"ids"`
	}
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	groupID, err := parseID(input.GroupID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "商品分组 ID 无效")
		return
	}
	ids, err := parseIDs(input.IDs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "商品排序 ID 无效")
		return
	}
	err = h.products.SortProducts(r.Context(), groupID, ids)
	writeAdminMutation(w, map[string]bool{"sorted": true}, err, "保存商品排序失败")
}
