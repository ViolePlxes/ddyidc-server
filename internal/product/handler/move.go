package product

import "net/http"

func (h *publicHandler) moveGroup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID             string   `json:"id"`
		TargetParentID string   `json:"targetParentId"`
		TargetIDs      []string `json:"targetIds"`
		SourceIDs      []string `json:"sourceIds"`
	}
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	id, err := parseID(input.ID)
	if err != nil {
		writeError(w, 400, "二级分组 ID 无效")
		return
	}
	target, err := parseID(input.TargetParentID)
	if err != nil {
		writeError(w, 400, "目标必须为一级分组")
		return
	}
	if input.TargetIDs == nil || input.SourceIDs == nil {
		writeError(w, 400, "必须提交目标层和源层全量顺序")
		return
	}
	targetIDs, err := parseIDs(input.TargetIDs)
	if err != nil {
		writeError(w, 400, "目标层顺序无效")
		return
	}
	sourceIDs, err := parseIDs(input.SourceIDs)
	if err != nil {
		writeError(w, 400, "源层顺序无效")
		return
	}
	err = h.products.MoveAdminGroup(r.Context(), id, target, targetIDs, sourceIDs)
	writeAdminMutation(w, map[string]bool{"moved": true}, err, "移动分组失败，全部变更已回滚")
}
