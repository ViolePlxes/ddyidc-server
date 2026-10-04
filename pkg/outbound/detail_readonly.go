package outbound

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DetailReadOnly is a deployment fence for the staged migration. It must be
// removed only as part of an explicitly approved business cutover.
func DetailReadOnly() bool { return current.Load().detailReadOnly }

// CheckDetailRequest runs before dialing, including on redirects. Some legacy
// suppliers mutate their cart through GET, so allowing all GETs is unsafe.
func CheckDetailRequest(r *http.Request) error {
	p := current.Load()
	if !p.detailReadOnly { return nil }
	if err := p.URL(r.URL.String()); err != nil { return err }
	if r.URL.Scheme != "https" || r.URL.RawPath != "" { return ErrDenied }
	path := r.URL.Path
	if r.Method == http.MethodPost && path == "/zjmf_api_login" && r.URL.RawQuery == "" { return nil }
	if r.Method != http.MethodGet { return ErrDenied }
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil { return ErrDenied }
	_, err = strconv.ParseInt(q.Get("host_id"), 10, 64)
	if path == "/host/header" && len(q) == 1 && len(q["host_id"]) == 1 && err == nil {
		id, _ := strconv.ParseInt(q.Get("host_id"), 10, 64)
		if id > 0 { return nil }
	}
	if path == "/api/product/list" && len(q) == 0 { return nil }
	if strings.HasPrefix(path, "/api/product/") {
		id, e := strconv.ParseInt(strings.TrimPrefix(path, "/api/product/"), 10, 64)
		if e == nil && id > 0 && len(q) == 1 && len(q["price_basis"]) == 1 && q.Get("price_basis") == "agent" { return nil }
	}
	const prefix = "/v10/host/product/"
	const suffix = "/mf_cloud/order_page"
	if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, suffix) {
		id, e := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix), 10, 64)
		if e == nil && id > 0 && len(q) == 2 && len(q["price_basis"]) == 1 && len(q["is_downstream"]) == 1 && q.Get("price_basis") == "agent" && q.Get("is_downstream") == "1" { return nil }
	}
	return ErrDenied
}
