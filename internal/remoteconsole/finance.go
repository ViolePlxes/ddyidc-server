package remoteconsole

import (
	"encoding/base64"
	"net/url"
	"strings"
)

// Finance providers wrap the actual socket URL in a noVNC HTML URL.
func unwrapNoVNC(raw, password string) (string, string, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return "", "", ErrUpstreamUnavailable
	}
	if strings.HasSuffix(u.Path, "/novnc") {
		encoded := u.Query().Get("url")
		decoded, e := base64.StdEncoding.DecodeString(encoded)
		if e != nil {
			return "", "", ErrUpstreamUnavailable
		}
		target, e := url.Parse(string(decoded))
		if e != nil || (target.Scheme != "wss" && target.Scheme != "ws") || target.Host == "" {
			return "", "", ErrUpstreamUnavailable
		}
		if password == "" {
			password = u.Query().Get("password")
		}
		return target.String(), password, nil
	}
	return raw, password, nil
}
