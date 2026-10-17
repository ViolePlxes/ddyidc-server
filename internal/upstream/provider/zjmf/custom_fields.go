package zjmf

import (
	"ddyidc/internal/upstream/provider/contracts"
	"encoding/json"
	"strconv"
	"strings"
)

func customFields(raw json.RawMessage) []contracts.CustomField {
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			raw = []byte(text)
		}
	}
	var list []struct {
		ID         json.RawMessage `json:"id"`
		Name       string          `json:"fieldname"`
		Label      string          `json:"name"`
		Type       string          `json:"fieldtype"`
		Required   json.RawMessage `json:"required"`
		Options    string          `json:"fieldoptions"`
		Validation string          `json:"regexpr"`
	}
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "[]" {
		return nil
	}
	if json.Unmarshal(raw, &list) != nil {
		return []contracts.CustomField{{ID: 1, Name: "商品附加信息", Type: "unsupported", Required: true}}
	}
	out := []contracts.CustomField{}
	for _, f := range list {
		id, e := strconv.Atoi(strings.Trim(string(f.ID), "\""))
		if e != nil || id <= 0 {
			return []contracts.CustomField{{ID: 1, Name: "商品附加信息", Type: "unsupported", Required: true}}
		}
		kind := f.Type
		if kind == "dropdown" {
			kind = "select"
		}
		if kind == "textarea" || kind == "link" {
			kind = "text"
		}
		req := strings.Trim(string(f.Required), "\"")
		field := contracts.CustomField{ID: id, Name: firstNonEmpty(f.Name, f.Label), Type: kind, Required: req == "1" || req == "true" || req == "on", Validation: f.Validation}
		if f.Options != "" {
			field.Choices = strings.Split(f.Options, ",")
		}
		out = append(out, field)
	}
	return out
}
