package admintodo

import (
	"context"
	"gorm.io/gorm"
	"sort"
)

// The legacy and new tables use different MySQL collations. Query each source
// independently instead of UNIONing their string columns across schemas.
func productProblems(ctx context.Context, db *gorm.DB, instanceID int64) ([]ProductProblem, error) {
	problems := []ProductProblem{}
	queries := []string{
		"SELECT CAST(id AS CHAR) id,'event' source,event_type action,CAST(status AS CHAR) status,updated_at FROM instance.instance_events WHERE instance_id=? AND status=3 AND event_type IN ('create','suspend','terminate','destroy','renew','sync_due_time','unsuspend') AND NOT EXISTS(SELECT 1 FROM system.todo_event_resolutions z WHERE z.event_id=instance.instance_events.id)",
		"SELECT CAST(id AS CHAR) id,'delivery' source,'create' action,status,updated_at FROM `order`.delivery_units WHERE instance_id=? AND status IN ('unknown','failed')",
		"SELECT CAST(id AS CHAR) id,'renewal' source,'renew' action,state status,updated_at FROM instance.instance_renewals WHERE instance_id=? AND state IN ('uncertain','failed','verifying')",
	}
	for _, query := range queries {
		var rows []ProductProblem
		if err := db.WithContext(ctx).Raw(query, instanceID).Scan(&rows).Error; err != nil {
			return nil, err
		}
		problems = append(problems, rows...)
	}
	sort.SliceStable(problems, func(i, j int) bool {
		if !problems[i].UpdatedAt.Equal(problems[j].UpdatedAt) {
			return problems[i].UpdatedAt.After(problems[j].UpdatedAt)
		}
		if problems[i].Source != problems[j].Source {
			return problems[i].Source < problems[j].Source
		}
		return problems[i].ID < problems[j].ID
	})
	return problems, nil
}
