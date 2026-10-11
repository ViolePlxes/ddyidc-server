package ksyun

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// Translate only documented supplier fields. Missing fields stay missing so the
// caller cannot mistake an incomplete response for verified configuration.
func normalizeReadback(module string, raw map[string]any) map[string]any {
	wrappers := map[string]string{"query_params": "IgnoreQueryStringConfig", "referer": "ReferProtectionConfig", "ip_acl": "IpProtectionConfig", "request_auth": "RequestAuthConfig", "origin_host": "BackOriginHostConfig", "http2": "Http2OptionConfig", "force_redirect": "ForceRedirectConfig", "tls_version": "TLSVersionConfig", "advanced_origin": "OriginAdvancedConfig", "cache_rules": "CacheRuleConfig", "error_page": "ErrorPageConfig"}
	if module == "cache_rules" || module == "error_page" {
		wrapper, array, local := "CacheRuleConfig", "CacheRules", "rules"
		mapping := map[string]string{"type": "CacheRuleType", "value": "Value", "enabled": "CacheEnable", "seconds": "CacheTime", "respectOrigin": "RespectOrigin"}
		if module == "error_page" {
			wrapper, array, local = "ErrorPageConfig", "ErrorPages", "pages"
			mapping = map[string]string{"code": "ErrorHttpCode", "url": "CustomPageUrl"}
		}
		result := map[string]any{}
		if container, ok := raw[wrapper].(map[string]any); ok {
			if rows, ok := container[array].([]any); ok {
				normalized := []any{}
				for _, item := range rows {
					row, ok := item.(map[string]any)
					if !ok {
						return result
					}
					mapped := map[string]any{}
					for key, remote := range mapping {
						if value, exists := row[remote]; exists {
							if key == "enabled" || key == "respectOrigin" {
								if value == "on" {
									value = true
								} else if value == "off" {
									value = false
								}
							}
							mapped[key] = value
						}
					}
					normalized = append(normalized, mapped)
				}
				result[local] = normalized
			}
		}
		return result
	}
	if module == "performance" {
		result := map[string]any{}
		for local, wrapper := range map[string]string{"gzip": "PageCompressConfig", "br": "BrCompressConfig"} {
			if source, ok := raw[wrapper].(map[string]any); ok {
				if value, exists := source["Enable"]; exists {
					if value == "on" {
						result[local] = true
					} else if value == "off" {
						result[local] = false
					}
				}
				if local == "gzip" {
					for key, remote := range map[string]string{"fileTypes": "FileType", "fileSize": "FileSize"} {
						if value, exists := source[remote]; exists {
							result[key] = value
						}
					}
				}
			}
		}
		return result
	}
	source := raw
	if wrapper, ok := wrappers[module]; ok {
		if nested, yes := raw[wrapper].(map[string]any); yes {
			source = nested
		}
	}
	fields := map[string]map[string]string{
		"advanced_origin": {"enabled": "Enable", "originType": "OriginType", "origin": "Origin", "backupOriginType": "BackupOriginType", "backupOrigin": "BackupOrigin", "policy": "OriginPolicy", "bestCount": "OriginPolicyBestCount"},
		"basic_origin":    {"accelerateRegion": "region", "originType": "originType", "originHost": "originHost", "originProtocol": "originProtocol"},
		"query_params":    {"enabled": "Enable", "type": "Type", "hashKeyArgs": "HashKeyArgs"},
		"referer":         {"enabled": "Enable", "type": "ReferType", "values": "ReferList", "allowEmpty": "AllowEmpty"},
		"ip_acl":          {"enabled": "Enable", "type": "IpType", "values": "IpList"},
		"request_auth":    {"enabled": "Enable", "authType": "AuthType", "primaryKey": "Key1", "backupKey": "Key2", "expireSeconds": "ExpirationTime"},
		"origin_host":     {"host": "BackOriginHost", "followRequestDomain": "FollowReqDomain"}, "http2": {"enabled": "Enable"},
		"force_redirect": {"type": "RedirectType", "code": "RedirectCode"}, "tls_version": {"versions": "TLSVersion"},
	}
	spec, ok := fields[module]
	if !ok {
		return raw
	}
	result := map[string]any{}
	for local, remote := range spec {
		value, yes := source[remote]
		if !yes {
			continue
		}
		switch local {
		case "accelerateRegion":
			if v, ok := value.(string); ok {
				if mapped, yes := map[string]string{"CN": "mainland", "OverSea": "overseas", "Global": "global"}[v]; yes {
					value = mapped
				}
			}
		case "authType":
			if value == "typeA" {
				value = "1"
			} else if value == "typeB" {
				value = "2"
			}
		case "originType":
			if module == "basic_origin" && value == "ipaddr" {
				value = "ip"
			}
		case "enabled", "allowEmpty", "followRequestDomain":
			if v, ok := value.(string); ok {
				if v == "on" || (local == "followRequestDomain" && v == "true") {
					value = true
				} else if v == "off" || (local == "followRequestDomain" && v == "false") {
					value = false
				}
			}
		case "values", "versions":
			if v, ok := value.(string); ok {
				items := []any{}
				for _, x := range strings.Split(v, ",") {
					if x = strings.TrimSpace(x); x != "" {
						items = append(items, x)
					}
				}
				value = items
			}
		case "expireSeconds":
			if v, ok := value.(string); ok {
				if n, e := strconv.ParseFloat(v, 64); e == nil {
					value = n
				}
			}
		case "type":
			if module == "referer" || module == "ip_acl" {
				if value == "allow" {
					value = "whitelist"
				} else if value == "block" {
					value = "blacklist"
				}
			}
		}
		result[local] = value
	}
	return result
}

func protectionType(config map[string]any) string {
	value := text(config, "type")
	switch value {
	case "whitelist":
		return "allow"
	case "blacklist":
		return "block"
	}
	return value
}

// SDK releases disagree on whether configurations are wrapped as JSON text.
func decodeDomainConfigs(response map[string]json.RawMessage) (map[string]any, error) {
	payload, err := json.Marshal(response)
	if err != nil {
		return nil, errors.New("金山云配置响应无法解析")
	}
	if wrapped, exists := response["DomainConfigs"]; exists && string(wrapped) != "null" {
		var encoded string
		if json.Unmarshal(wrapped, &encoded) == nil {
			payload = []byte(encoded)
		} else {
			payload = wrapped
		}
	}
	var out map[string]any
	if json.Unmarshal(payload, &out) != nil || out == nil {
		return nil, errors.New("金山云配置响应无法解析")
	}
	return out, nil
}
