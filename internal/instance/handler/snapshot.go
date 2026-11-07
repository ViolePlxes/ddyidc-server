package instance

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/zeromicro/go-zero/rest/pathvar"
)

func (h *handler) snapshots(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	list, err := h.service.Snapshots(r.Context(), customerID, instanceID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, list)
}

func (h *handler) createSnapshot(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil {
		writeError(w, http.StatusBadRequest, "请求参数无效")
		return
	}
	if err := h.service.CreateSnapshot(r.Context(), customerID, instanceID, strings.TrimSpace(input.Name)); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"created": true})
}

func (h *handler) restoreSnapshot(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	snapshotID := strings.TrimSpace(pathvar.Vars(r)["snapshotId"])
	if snapshotID == "" {
		writeError(w, http.StatusBadRequest, "快照编号无效")
		return
	}
	if err := h.service.RestoreSnapshot(r.Context(), customerID, instanceID, snapshotID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"restored": true})
}

func (h *handler) deleteSnapshot(w http.ResponseWriter, r *http.Request) {
	customerID, ok := h.claims(w, r)
	if !ok {
		return
	}
	instanceID := pathID(w, r)
	if instanceID == 0 {
		return
	}
	snapshotID := strings.TrimSpace(pathvar.Vars(r)["snapshotId"])
	if snapshotID == "" {
		writeError(w, http.StatusBadRequest, "快照编号无效")
		return
	}
	if err := h.service.DeleteSnapshot(r.Context(), customerID, instanceID, snapshotID); err != nil {
		writeServiceError(w, err)
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}
