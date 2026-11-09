package visitanalytics

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Rollup dimensions. Each stored value is a joined label tuple so a single
// pre-aggregate table can answer every administrator query without scanning
// raw events.
const (
	dimAll            = "all"
	dimFlow           = "flow"
	dimStatus         = "status"
	dimGeo            = "geo"
	dimIdentity       = "identity"
	dimClientType     = "clienttype"
	dimIdentityClient = "ident_client"
	dimClient         = "client"

	// keySeparator joins label tuples inside dimension_value. It is a control
	// character that cannot appear in sanitized paths, titles or hosts.
	keySeparator = "\x1f"

	maxPathLength    = 512
	maxRouteLength   = 120
	maxHostLength    = 128
	maxTitleLength   = 128
	maxRefererLength = 512
	maxSourceLength  = 64
	maxLabelLength   = 64
	maxDimensionLen  = 384
	maxRequestIDLen  = 64
)

// Record is one collected visit. It contains no raw address, no raw User-Agent
// and no query string: every field is a bounded label or an HMAC digest.
type Record struct {
	OccurredAt   time.Time
	RequestID    string
	Host         string
	Method       string
	Path         string
	Route        string
	RefererHost  string
	RefererPath  string
	PageTitle    string
	Source       string
	IdentityType string
	IdentityID   int64
	StatusCode   int
	DurationMS   int
	ClientType   string
	ClientLabel  string
	PageLabel    string
	Browser      string
	OS           string
	Device       string
	IsBot        bool
	IPHash       string
	Country      string
	Region       string
	City         string
}

type recordInput struct {
	At           time.Time
	RequestID    string
	Host         string
	Method       string
	Path         string
	RefererPath  string
	RefererHost  string
	PageTitle    string
	UTM          string
	IdentityType string
	IdentityID   int64
	StatusCode   int
	DurationMS   int
	Client       Client
	IPHash       string
	Country      string
	Region       string
	City         string
	API          bool
}

func buildRecord(in recordInput) Record {
	route := normalizeRoute(in.Path)
	pageLabel := route
	if in.API {
		// Interface calls are bucketed so the page stage of the visit flow keeps
		// showing pages; the exact API route stays in the route column.
		pageLabel = "接口调用"
	}
	source := SourceLabel(in.RefererHost, in.UTM, in.Host)
	clientType := in.Client.Kind
	if clientType == "" {
		clientType = KindBrowser
	}
	record := Record{
		OccurredAt:   in.At,
		RequestID:    truncate(in.RequestID, maxRequestIDLen),
		Host:         truncate(strings.ToLower(in.Host), maxHostLength),
		Method:       truncate(strings.ToUpper(in.Method), 8),
		Path:         maskAdminEntry(sanitizePath(in.Path)),
		Route:        route,
		RefererHost:  truncate(strings.ToLower(in.RefererHost), maxHostLength),
		RefererPath:  maskAdminEntry(sanitizePath(in.RefererPath)),
		PageTitle:    sanitizeText(in.PageTitle, maxTitleLength),
		Source:       truncate(source, maxSourceLength),
		IdentityType: identityTypeOrDefault(in.IdentityType),
		IdentityID:   in.IdentityID,
		StatusCode:   in.StatusCode,
		DurationMS:   in.DurationMS,
		ClientType:   clientType,
		ClientLabel:  truncate(clientLabel(in.Client), maxLabelLength),
		PageLabel:    truncate(pageLabel, maxRouteLength),
		Browser:      truncate(in.Client.Browser, 32),
		OS:           truncate(in.Client.OS, 32),
		Device:       truncate(in.Client.Device, 16),
		IsBot:        in.Client.IsBot,
		IPHash:       truncate(in.IPHash, 64),
		Country:      truncate(defaultLabel(in.Country, "unknown"), 64),
		Region:       truncate(in.Region, 64),
		City:         truncate(in.City, 64),
	}
	if record.DurationMS < 0 {
		record.DurationMS = 0
	}
	return record
}

func identityTypeOrDefault(value string) string {
	switch value {
	case "customer", "admin":
		return value
	default:
		return "guest"
	}
}

func defaultLabel(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// clientLabel is the second flow stage: the browser name, the crawler name or
// the literal API marker.
func clientLabel(client Client) string {
	switch client.Kind {
	case KindBot:
		if client.BotName != "" {
			return client.BotName
		}
		return "其他爬虫"
	case KindAPI:
		return "API"
	default:
		if client.Browser == "" {
			return labelOther
		}
		return client.Browser
	}
}

// aggregateRows lists the pre-aggregated slices a record contributes to.
func (r Record) aggregateRows() []struct{ Dimension, Value string } {
	identity := r.IdentityType
	clientType := r.ClientType
	rows := []struct{ Dimension, Value string }{
		{dimAll, ""},
		{dimIdentity, identity},
		{dimClientType, clientType},
		{dimIdentityClient, identity + keySeparator + clientType},
		{dimStatus, statusLabel(r.StatusCode)},
		{dimGeo, strings.Join([]string{r.Country, r.Region, r.City}, keySeparator)},
		{dimClient, strings.Join([]string{r.Browser, r.OS, r.Device, boolLabel(r.IsBot)}, keySeparator)},
		{dimFlow, r.FlowValue()},
	}
	for i := range rows {
		if len(rows[i].Value) > maxDimensionLen {
			rows[i].Value = truncate(rows[i].Value, maxDimensionLen)
		}
	}
	return rows
}

// FlowValue is the source｜client｜page｜identity｜client-type tuple used for the
// flow chart and for every dimension marginal without an extra scan. The client
// type is the trailing field so the first four marginal positions stay stable.
func (r Record) FlowValue() string {
	return strings.Join([]string{r.Source, r.ClientLabel, r.PageLabel, r.IdentityType, r.ClientType}, keySeparator)
}

func statusLabel(code int) string {
	if code <= 0 {
		return "0"
	}
	return strconv.Itoa(code)
}

func boolLabel(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// sanitizePath keeps the path shape but strips query strings, control
// characters and over-long values.
func sanitizePath(raw string) string {
	if raw == "" {
		return "/"
	}
	if index := strings.IndexAny(raw, "?#"); index >= 0 {
		raw = raw[:index]
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	clean := strings.Map(func(symbol rune) rune {
		if symbol < 0x20 || symbol == 0x7f {
			return -1
		}
		return symbol
	}, raw)
	return truncate(clean, maxPathLength)
}

// sanitizeText keeps a short, printable label. Control characters and angle
// brackets are dropped so stored labels cannot break exports or markup.
func sanitizeText(raw string, limit int) string {
	if raw == "" {
		return ""
	}
	clean := strings.Map(func(symbol rune) rune {
		if symbol < 0x20 || symbol == 0x7f {
			return ' '
		}
		if symbol == '<' || symbol == '>' {
			return -1
		}
		if !unicode.IsPrint(symbol) {
			return -1
		}
		return symbol
	}, raw)
	return truncate(strings.Join(strings.Fields(clean), " "), limit)
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	// Cut on a rune boundary so the stored label stays valid UTF-8.
	cut := limit
	for cut > 0 && !utf8Start(value[cut]) {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return value[:cut]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// normalizeRoute collapses dynamic identifiers so routes aggregate into a
// bounded, human-readable set.
func normalizeRoute(rawPath string) string {
	path := sanitizePath(rawPath)
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if segment == "" {
			continue
		}
		segments[i] = normalizeSegment(segment)
	}
	route := strings.Join(segments, "/")
	if route == "" {
		route = "/"
	}
	return truncate(route, maxRouteLength)
}

func normalizeSegment(segment string) string {
	if isAdminEntrySegment(segment) {
		return "dd-admin-*"
	}
	if len(segment) > maxRouteLength {
		return truncate(segment, 32) + "…"
	}
	if isNumeric(segment) || isUUID(segment) || isLongHex(segment) {
		return ":id"
	}
	if looksLikeSlug(segment) || looksRandom(segment) {
		return ":slug"
	}
	return segment
}

func isNumeric(value string) bool {
	if value == "" || len(value) > 19 {
		return false
	}
	for _, symbol := range value {
		if symbol < '0' || symbol > '9' {
			return false
		}
	}
	return true
}

// isAdminEntrySegment recognises the randomized administrator entry prefix. The
// value is a secret path component and is replaced before anything is stored.
func isAdminEntrySegment(segment string) bool {
	lower := strings.ToLower(segment)
	const prefix = "dd-admin-"
	if !strings.HasPrefix(lower, prefix) || len(segment) <= len(prefix) || len(segment) > 72 {
		return false
	}
	for _, symbol := range lower[len(prefix):] {
		if (symbol < 'a' || symbol > 'z') && (symbol < '0' || symbol > '9') {
			return false
		}
	}
	return true
}

// maskAdminEntry replaces every administrator entry segment in a stored path.
func maskAdminEntry(path string) string {
	if !strings.Contains(strings.ToLower(path), "/dd-admin-") {
		return path
	}
	segments := strings.Split(path, "/")
	for index, segment := range segments {
		if isAdminEntrySegment(segment) {
			segments[index] = "dd-admin-*"
		}
	}
	return strings.Join(segments, "/")
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, symbol := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if symbol != '-' {
				return false
			}
			continue
		}
		if !isHexRune(symbol) {
			return false
		}
	}
	return true
}

func isLongHex(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, symbol := range value {
		if !isHexRune(symbol) {
			return false
		}
	}
	return true
}

func isHexRune(symbol rune) bool {
	return (symbol >= '0' && symbol <= '9') || (symbol >= 'a' && symbol <= 'f') || (symbol >= 'A' && symbol <= 'F')
}

func looksLikeSlug(value string) bool {
	return strings.Contains(value, "-") && len(value) > 24
}

// looksRandom collapses opaque tokens (mixed case and digits without
// separators) so a scanner cannot create unbounded page cardinality.
func looksRandom(value string) bool {
	if len(value) < 24 {
		return false
	}
	hasDigit, hasLetter := false, false
	for _, symbol := range value {
		switch {
		case symbol >= '0' && symbol <= '9':
			hasDigit = true
		case (symbol >= 'a' && symbol <= 'z') || (symbol >= 'A' && symbol <= 'Z'):
			hasLetter = true
		}
	}
	return hasDigit && hasLetter && !strings.Contains(value, ".")
}

// ParseReferer returns the referer host and path. The query string is dropped
// because it may carry credentials from another site.
func ParseReferer(raw string) (host, path string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if index := strings.IndexAny(raw, "?#"); index >= 0 {
		raw = raw[:index]
	}
	scheme := ""
	if index := strings.Index(raw, "://"); index >= 0 {
		scheme = strings.ToLower(raw[:index])
		raw = raw[index+3:]
	}
	if scheme != "" && scheme != "http" && scheme != "https" {
		return "", ""
	}
	if cut := strings.Index(raw, "/"); cut >= 0 {
		host, path = raw[:cut], raw[cut:]
	} else {
		host = raw
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", ""
	}
	// Referer URLs may carry userinfo; credentials must never reach the table.
	if index := strings.LastIndex(host, "@"); index >= 0 {
		host = host[index+1:]
	}
	if parsedHost, port, err := net.SplitHostPort(host); err == nil {
		// SplitHostPort is lenient about the port value, so it is validated here.
		if !isNumeric(port) {
			return "", ""
		}
		if strings.Contains(parsedHost, ":") {
			parsedHost = "[" + parsedHost + "]"
		}
		host = parsedHost
	} else if index := strings.LastIndex(host, ":"); index > 0 && isNumeric(host[index+1:]) {
		host = host[:index]
	}
	if !validHost(host) {
		return "", ""
	}
	return host, truncate(sanitizePath(path), maxRefererLength)
}

// validHost accepts a DNS name, an IPv4 literal or a bracketed IPv6 literal.
func validHost(host string) bool {
	if host == "" || len(host) > maxHostLength {
		return false
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		address, err := netip.ParseAddr(host[1 : len(host)-1])
		return err == nil && address.Is6()
	}
	if strings.Contains(host, ":") {
		return false
	}
	for _, symbol := range host {
		switch {
		case symbol >= 'a' && symbol <= 'z', symbol >= '0' && symbol <= '9',
			symbol == '.', symbol == '-', symbol == '_':
		default:
			return false
		}
	}
	return true
}

// SourceLabel applies the documented priority: referer host, then utm_source,
// then an internal navigation, then a direct visit.
func SourceLabel(refererHost, utm, requestHost string) string {
	refererHost = strings.ToLower(strings.TrimSpace(refererHost))
	if refererHost != "" {
		if SameSite(refererHost, requestHost) {
			return "站内跳转"
		}
		return truncate(refererHost, maxSourceLength)
	}
	if utm := sanitizeText(utm, 32); utm != "" {
		return "utm:" + utm
	}
	return "直接访问"
}

// SameSite compares the registrable suffix of two hosts. It intentionally
// ignores public suffixes beyond the last two labels: the value only decides
// "internal navigation" versus an external source label.
func SameSite(left, right string) bool {
	left = normalizeHost(left)
	right = normalizeHost(right)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	leftLabels := strings.Split(left, ".")
	rightLabels := strings.Split(right, ".")
	if len(leftLabels) < 2 || len(rightLabels) < 2 {
		return false
	}
	return leftLabels[len(leftLabels)-2] == rightLabels[len(rightLabels)-2] &&
		leftLabels[len(leftLabels)-1] == rightLabels[len(rightLabels)-1]
}

func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	return strings.TrimPrefix(strings.TrimSuffix(host, "."), "www.")
}
