package visitanalytics

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// beaconPayload is the only client-supplied data the collector accepts. Titles
// and referrers are trimmed to labels before they are stored.
type beaconPayload struct {
	Path     string `json:"path"`
	Referrer string `json:"referrer"`
	Title    string `json:"title"`
	UTM      string `json:"utm"`
}

const (
	beaconMaxBody = 4096
	beaconMethod  = http.MethodPost
)

// handleBeacon records a client-side page view. The SPA shell is served by the
// web server, so server middleware alone can never see a page visit; the browser
// reports the route and the server derives every other field.
func (s *Service) handleBeacon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// The response is intentionally identical for accepted, sampled and invalid
	// beacons so the endpoint cannot be used to probe collection state.
	w.WriteHeader(http.StatusNoContent)
	if !s.Enabled() || r.Method != beaconMethod {
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, beaconMaxBody+1))
	if err != nil || len(body) > beaconMaxBody {
		return
	}
	var payload beaconPayload
	if json.Unmarshal(body, &payload) != nil {
		return
	}
	path := sanitizePath(payload.Path)
	if !strings.HasPrefix(payload.Path, "/") || strings.HasPrefix(payload.Path, "//") {
		return
	}
	// Only page routes are accepted: an API path would double count an ordinary
	// request and a beacon can never describe one.
	if IsAPIPath(path) || excludedPath(path) || !collectablePath(path) {
		return
	}
	if !sampled(s.cfg.PageSamplePercent) {
		s.sampled.Add(1)
		return
	}
	if !s.limiter.allow(s.now()) {
		s.dropped.Add(1)
		return
	}
	refererHost, refererPath := ParseReferer(payload.Referrer)
	s.enqueue(s.makeRecord(recordRequest{
		request:     r,
		path:        path,
		refererHost: refererHost,
		refererPath: refererPath,
		utm:         payload.UTM,
		title:       payload.Title,
		status:      http.StatusOK,
		duration:    0,
		requestID:   r.Header.Get("X-Request-ID"),
		api:         false,
		location:    s.locationFor(r),
	}))
}
