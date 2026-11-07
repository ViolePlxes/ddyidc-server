package instance

import (
	"encoding/json"
	"net/http"
	"strings"

	"ddyidc/internal/upstream/provider/contracts"
	"github.com/zeromicro/go-zero/rest/pathvar"
)

func (h *handler) natRules(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	rules, err := h.service.NATRules(r.Context(), customerID, instanceID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, rules)
}

func (h *handler) createNATRule(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	var input contracts.NATRuleInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		writeError(w, http.StatusBadRequest, "请求参数无效")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	rule, err := h.service.CreateNATRule(r.Context(), customerID, instanceID, input)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, rule)
}

func (h *handler) deleteNATRule(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	ruleID := strings.TrimSpace(pathvar.Vars(r)["ruleId"])
	if ruleID == "" {
		writeError(w, http.StatusBadRequest, "NAT 规则编号无效")
		return
	}
	if err := h.service.DeleteNATRule(r.Context(), customerID, instanceID, ruleID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}
