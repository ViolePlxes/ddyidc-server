package zjmf

import "strings"

// Only trust explicit module declarations, never supplier HTML or labels.
func hasModuleAction(value any, action string) bool {
	switch data := value.(type) {
	case map[string]any:
		if disabled, ok := data["disabled"].(bool); ok && disabled {
			return false
		}
		if fn, ok := data["func"].(string); ok && strings.EqualFold(fn, action) {
			return true
		}
		for key, child := range data {
			if strings.EqualFold(key, action) {
				if item, ok := child.(map[string]any); ok {
					if disabled, ok := item["disabled"].(bool); ok && disabled {
						continue
					}
					return true
				}
			}
			if hasModuleAction(child, action) {
				return true
			}
		}
	case []any:
		for _, item := range data {
			if hasModuleAction(item, action) {
				return true
			}
		}
	}
	return false
}
