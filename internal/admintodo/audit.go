package admintodo

import "gorm.io/gorm"

// auditReplay binds a request key to its original actor, object and action.
// A reused key with different inputs must never authorize another mutation.
func auditReplay(tx *gorm.DB, request, kind, source string, id, admin int64, action, notes, evidence string) (bool, error) {
	var row struct {
		Kind, Source, Action, Notes, Evidence string
		ObjectID, AdminID                     int64
	}
	err := tx.Table("system.todo_audits").Where("request_id=?", request).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.Kind != kind || row.Source != source || row.ObjectID != id || row.AdminID != admin || row.Action != action || row.Notes != notes || row.Evidence != evidence {
		return false, ErrConflict
	}
	return true, nil
}
