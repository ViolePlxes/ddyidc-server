package visitanalytics

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ddyidc/pkg/auth"
	"ddyidc/pkg/clientip"
)

// Service owns the collection pipeline: request sampling, the in-memory queue,
// the batched writer and the offline geo table.
type Service struct {
	cfg      Config
	database *sql.DB
	jwt      *auth.Manager
	key      []byte
	geo      *geoStore
	queue    chan Record
	limiter  *rateLimiter
	now      func() time.Time

	accepted      atomic.Int64
	dropped       atomic.Int64
	sampled       atomic.Int64
	written       atomic.Int64
	writeFailures atomic.Int64

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// New builds a collector. A nil database yields a disabled service so tests and
// single-command modes never allocate the pipeline.
func New(cfg Config, database *sql.DB, jwt *auth.Manager, secret []byte) *Service {
	cfg = cfg.normalized()
	if database == nil {
		cfg.Disabled = true
	}
	service := &Service{
		cfg:      cfg,
		database: database,
		jwt:      jwt,
		key:      deriveIPKey(secret),
		geo:      newGeoStore(cfg.GeoIPFile),
		limiter:  &rateLimiter{limit: cfg.MaxEventsPerSecond},
		now:      time.Now,
		done:     make(chan struct{}),
	}
	service.queue = make(chan Record, cfg.QueueSize)
	return service
}

// Enabled reports whether the collector records events.
func (s *Service) Enabled() bool { return s != nil && !s.cfg.Disabled && s.database != nil }

// deriveIPKey scopes the address digest to this deployment and purpose.
func deriveIPKey(secret []byte) []byte {
	digest := sha256.New()
	digest.Write([]byte("ddyidc.visit-analytics.ip.v1\x00"))
	digest.Write(secret)
	return digest.Sum(nil)
}

// HashIP returns the HMAC-SHA256 digest stored instead of the raw address.
func HashIP(key []byte, address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(address))
	return hex.EncodeToString(mac.Sum(nil))
}

type rateLimiter struct {
	mu          sync.Mutex
	limit       int
	windowStart time.Time
	used        int
}

func (l *rateLimiter) allow(now time.Time) bool {
	if l.limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windowStart.IsZero() || now.Sub(l.windowStart) >= time.Second {
		l.windowStart = now
		l.used = 0
	}
	if l.used >= l.limit {
		return false
	}
	l.used++
	return true
}

// Stats exposes counters for administrator diagnostics.
func (s *Service) Stats() map[string]any {
	if s == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled":       s.Enabled(),
		"accepted":      s.accepted.Load(),
		"sampledOut":    s.sampled.Load(),
		"dropped":       s.dropped.Load(),
		"written":       s.written.Load(),
		"writeFailures": s.writeFailures.Load(),
		"queueDepth":    len(s.queue),
		"queueCapacity": cap(s.queue),
		"geo":           s.geo.GeoStatus(),
		"retentionDays": s.cfg.RetentionDays,
		"apiSamplePct":  s.cfg.APISamplePercent,
		"pageSamplePct": s.cfg.PageSamplePercent,
		"maxPerSecond":  s.cfg.MaxEventsPerSecond,
	}
}

// Middleware records the request and its response without ever blocking or
// failing business traffic.
func (s *Service) Middleware() func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !s.Enabled() || skippedPath(r.URL.Path) {
				next(w, r)
				return
			}
			started := s.now()
			recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
			next(recorder, r)
			if recorder.hijacked {
				return
			}
			s.observe(r, recorder.status, recorder.Header().Get("X-Request-ID"), s.now().Sub(started))
		}
	}
}

// observe applies the collection scope, sampling and rate limit, then enqueues.
func (s *Service) observe(r *http.Request, status int, requestID string, duration time.Duration) {
	path := r.URL.Path
	api := IsAPIPath(path)
	percent := s.cfg.PageSamplePercent
	if api {
		percent = s.cfg.APISamplePercent
	}
	if !sampled(percent) {
		s.sampled.Add(1)
		return
	}
	if !s.limiter.allow(s.now()) {
		s.dropped.Add(1)
		return
	}
	refererHost, refererPath := ParseReferer(r.Header.Get("Referer"))
	location := s.locationFor(r)
	s.enqueue(s.makeRecord(recordRequest{
		request:     r,
		path:        path,
		refererHost: refererHost,
		refererPath: refererPath,
		utm:         r.URL.Query().Get("utm_source"),
		status:      status,
		duration:    duration,
		requestID:   requestID,
		api:         api,
		location:    location,
	}))
}

type recordRequest struct {
	request     *http.Request
	path        string
	refererHost string
	refererPath string
	utm         string
	title       string
	status      int
	duration    time.Duration
	requestID   string
	api         bool
	location    Location
}

// makeRecord assembles the persisted shape. Everything identifying is either a
// bounded label or an HMAC digest; no raw address or User-Agent is retained.
func (s *Service) makeRecord(in recordRequest) Record {
	kind, claims := s.session(in.request)
	identityType, identityID := identityFromSession(kind, claims)
	return buildRecord(recordInput{
		At:           s.now(),
		RequestID:    in.requestID,
		Host:         in.request.Host,
		Method:       in.request.Method,
		Path:         in.path,
		RefererHost:  in.refererHost,
		RefererPath:  in.refererPath,
		PageTitle:    in.title,
		UTM:          in.utm,
		IdentityType: identityType,
		IdentityID:   identityID,
		StatusCode:   in.status,
		DurationMS:   int(in.duration.Milliseconds()),
		Client:       ParseClient(in.request.UserAgent()),
		IPHash:       s.hashRequest(in.request),
		Country:      in.location.Country,
		Region:       in.location.Region,
		City:         in.location.City,
		API:          in.api,
	})
}

func identityFromSession(kind auth.SessionKind, claims *auth.Claims) (string, int64) {
	switch kind {
	case auth.SessionAdmin, auth.SessionSales:
		if claims == nil {
			return "guest", 0
		}
		return "admin", claims.UserID
	case auth.SessionUser:
		if claims == nil {
			return "guest", 0
		}
		return "customer", claims.UserID
	default:
		return "guest", 0
	}
}

func sampled(percent float64) bool {
	if percent >= 100 {
		return true
	}
	if percent <= 0 {
		return false
	}
	return mathrand.Float64()*100 < percent
}

func (s *Service) enqueue(record Record) {
	select {
	case s.queue <- record:
		s.accepted.Add(1)
	default:
		s.dropped.Add(1)
	}
}

func (s *Service) hashRequest(r *http.Request) string {
	return HashIP(s.key, clientip.FromRequest(r))
}

func (s *Service) locationFor(r *http.Request) Location {
	address, err := netip.ParseAddr(clientip.FromRequest(r))
	if err != nil {
		return Location{Country: "unknown"}
	}
	return s.geo.Lookup(address)
}

// session resolves the visitor identity. The cookie middleware already validated
// the session for API prefixes, so no second validation happens there; the
// beacon arrives on the customer prefix and probes the remaining cookie kinds.
func (s *Service) session(r *http.Request) (auth.SessionKind, *auth.Claims) {
	if kind, claims, ok := auth.SessionFrom(r.Context()); ok {
		return kind, claims
	}
	if s.jwt == nil {
		return "", nil
	}
	for _, kind := range []auth.SessionKind{auth.SessionAdmin, auth.SessionSales, auth.SessionUser} {
		cookie, err := r.Cookie(s.jwt.CookieName(kind))
		if err != nil || cookie.Value == "" {
			continue
		}
		claims, err := s.jwt.ParseAccessTokenFor(r.Context(), kind, cookie.Value)
		if err == nil && claims != nil {
			return kind, claims
		}
	}
	return "", nil
}

// StartWriter launches the asynchronous batch writer. It is idempotent.
func (s *Service) StartWriter(ctx context.Context) {
	if s == nil || s.database == nil {
		return
	}
	s.once.Do(func() {
		runCtx, cancel := context.WithCancel(ctx)
		s.cancel = cancel
		go s.writeLoop(runCtx)
	})
}

// Close flushes the queue and stops the writer.
func (s *Service) Close() {
	if s == nil || s.cancel == nil {
		return
	}
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
	}
}

func (s *Service) writeLoop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(s.cfg.flushInterval())
	defer ticker.Stop()
	batch := make([]Record, 0, s.cfg.BatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		pending := batch
		batch = make([]Record, 0, s.cfg.BatchSize)
		if err := s.flush(pending); err != nil {
			s.writeFailures.Add(1)
			log.Printf("visit_analytics_write_failed count=%d error=%v", len(pending), err)
			return
		}
		s.written.Add(int64(len(pending)))
	}
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case record := <-s.queue:
					batch = append(batch, record)
					if len(batch) >= s.cfg.BatchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		case record := <-s.queue:
			batch = append(batch, record)
			if len(batch) >= s.cfg.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// flush writes one batch inside a single transaction so the pre-aggregates can
// never drift from the raw events.
func (s *Service) flush(batch []Record) error {
	if s.database == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	transaction, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	if err = insertEvents(ctx, transaction, batch); err != nil {
		return err
	}
	if err = upsertDaily(ctx, transaction, batch); err != nil {
		return err
	}
	return transaction.Commit()
}

// responseRecorder captures the status code without changing the response.
type responseRecorder struct {
	http.ResponseWriter
	status   int
	wrote    bool
	hijacked bool
}

func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseRecorder) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		if code >= 200 || code == http.StatusSwitchingProtocols {
			w.wrote = true
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseRecorder) Write(data []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *responseRecorder) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, buffer, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.hijacked = true
		w.status = http.StatusSwitchingProtocols
		w.wrote = true
	}
	return connection, buffer, err
}

// IsAPIPath reports whether the request targets a backend API prefix.
func IsAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/admin/v1/")
}

// collectablePath is the documented collection scope.
func collectablePath(path string) bool {
	if path == "" || isStaticAsset(path) {
		return false
	}
	if IsAPIPath(path) {
		return true
	}
	switch {
	case path == "/", path == "/products", strings.HasPrefix(path, "/products/"),
		strings.HasPrefix(path, "/news/"), strings.HasPrefix(path, "/help/"),
		strings.HasPrefix(path, "/console"), path == "/cart", path == "/order/confirm",
		strings.HasPrefix(path, "/billing"), strings.HasPrefix(path, "/wallet/"),
		strings.HasPrefix(path, "/domain"), strings.HasPrefix(path, "/user/"),
		strings.HasPrefix(path, "/sales"), strings.HasPrefix(path, "/dd-admin-"):
		return true
	default:
		return false
	}
}

const beaconPath = "/api/v1/visit/collect"

// excludedPath lists probes, payment callbacks, streams and static assets.
func excludedPath(path string) bool {
	switch path {
	case "/health", "/health/live", "/health/ready", "/ready", "/metrics", "/version.json":
		return true
	}
	if strings.HasPrefix(path, "/_ddyidc") {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/payment/notify/") || path == "/api/v1/payment/alipay/notify" {
		return true
	}
	if strings.Contains(path, "/webhook") || strings.Contains(path, "/callback") {
		return true
	}
	if strings.Contains(path, "/vnc/ws") || path == "/api/v1/notifications/events" {
		return true
	}
	// The analytics API must not observe itself: one admin page view issues
	// several queries and would inflate every counter it reports.
	if strings.HasPrefix(path, "/admin/v1/visit-analytics/") {
		return true
	}
	return isStaticAsset(path)
}

// skippedPath is the middleware guard: explicit exclusions plus the scope.
func skippedPath(path string) bool {
	if path == beaconPath || excludedPath(path) {
		return true
	}
	return !collectablePath(path)
}

func isStaticAsset(path string) bool {
	for _, extension := range []string{".js", ".css", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".ico", ".woff", ".woff2", ".ttf", ".otf", ".mp4", ".webm"} {
		if strings.HasSuffix(path, extension) {
			return true
		}
	}
	return strings.HasPrefix(path, "/assets/") || strings.HasPrefix(path, "/portal-media/")
}
