package ksyun

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// Some official CDN responses encode integer fields as strings, while the
// generated SDK declares integers. Convert only schema-declared numeric fields;
// preserve arbitrary strings and reject invalid/range-overflow values later.
func sdkCompatibleJSON[T any](body []byte) ([]byte, error) {
	var raw any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if e := decoder.Decode(&raw); e != nil {
		return nil, e
	}
	raw = normalizeSDKValue(raw, reflect.TypeOf((*T)(nil)).Elem())
	return json.Marshal(raw)
}
func normalizeSDKValue(value any, typ reflect.Type) any {
	if value == nil {
		return nil
	}
	optional := typ.Kind() == reflect.Pointer
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return value
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")[0]
			if tag == "-" {
				continue
			}
			if field.Anonymous && tag == "" {
				normalizeSDKValue(object, field.Type)
				continue
			}
			if tag == "" {
				tag = field.Name
			}
			for name, item := range object {
				if strings.EqualFold(name, tag) {
					object[name] = normalizeSDKValue(item, field.Type)
					break
				}
			}
		}
	case reflect.Slice, reflect.Array:
		if rows, ok := value.([]any); ok {
			for i, item := range rows {
				rows[i] = normalizeSDKValue(item, typ.Elem())
			}
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if text, ok := value.(string); ok {
			if text == "" && optional {
				return nil
			}
			if _, e := strconv.ParseInt(text, 10, typ.Bits()); e == nil {
				return json.Number(text)
			}
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if text, ok := value.(string); ok {
			if text == "" && optional {
				return nil
			}
			if _, e := strconv.ParseUint(text, 10, typ.Bits()); e == nil {
				return json.Number(text)
			}
		}
	case reflect.Float32, reflect.Float64:
		if text, ok := value.(string); ok {
			if text == "" && optional {
				return nil
			}
			if _, e := strconv.ParseFloat(text, typ.Bits()); e == nil {
				return json.Number(text)
			}
		}
	}
	return value
}
