package visitanalytics

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/netip"
	"os"
	"strings"
	"time"
)

// nginxLine is one JSON record of the dedicated visit log. The web server
// serves the SPA shell, so a crawler that never runs JavaScript only exists in
// the access log; this reader turns those lines into visit events.
type nginxLine struct {
	Time   string  `json:"time"`
	Host   string  `json:"host"`
	IP     string  `json:"ip"`
	Method string  `json:"method"`
	URI    string  `json:"uri"`
	Status int     `json:"status"`
	RT     float64 `json:"rt"`
	UA     string  `json:"ua"`
	Ref    string  `json:"ref"`
}

// ParseNginxLine decodes one JSON log record. It rejects anything that is not a
// page request already covered by the collector or the browser beacon.
func ParseNginxLine(raw string) (nginxLine, bool) {
	var line nginxLine
	if json.Unmarshal([]byte(raw), &line) != nil {
		return nginxLine{}, false
	}
	if !strings.HasPrefix(line.URI, "/") || len(line.URI) > maxPathLength {
		return nginxLine{}, false
	}
	return line, true
}

// nginxClient selects the only traffic the access log adds: clients that never
// run the beacon. Browsers are recorded once, by the browser itself.
func nginxClient(line nginxLine) (Client, bool) {
	client := ParseClient(line.UA)
	if client.Kind == KindBrowser {
		return client, false
	}
	return client, true
}

func (s *Service) ingestNginxLine(raw string) bool {
	if !s.Enabled() {
		return false
	}
	line, ok := ParseNginxLine(raw)
	if !ok {
		return false
	}
	// Interface traffic is collected by the request middleware; the access log
	// only adds the static SPA shell requests that never reach the API.
	if IsAPIPath(line.URI) || skippedPath(line.URI) {
		return false
	}
	client, ok := nginxClient(line)
	if !ok {
		return false
	}
	if !sampled(s.cfg.PageSamplePercent) {
		s.sampled.Add(1)
		return false
	}
	if !s.limiter.allow(s.now()) {
		s.dropped.Add(1)
		return false
	}
	at := s.now()
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(line.Time)); err == nil {
		at = parsed.In(time.Local)
	}
	refererHost, refererPath := ParseReferer(line.Ref)
	location := Location{Country: "unknown"}
	if address, err := netip.ParseAddr(strings.TrimSpace(line.IP)); err == nil {
		location = s.geo.Lookup(address)
	}
	duration := time.Duration(line.RT * float64(time.Second))
	if duration < 0 {
		duration = 0
	}
	record := buildRecord(recordInput{
		At:           at,
		Host:         line.Host,
		Method:       line.Method,
		Path:         line.URI,
		RefererHost:  refererHost,
		RefererPath:  refererPath,
		IdentityType: "guest",
		StatusCode:   line.Status,
		DurationMS:   int(duration.Milliseconds()),
		Client:       client,
		IPHash:       HashIP(s.key, line.IP),
		Country:      location.Country,
		Region:       location.Region,
		City:         location.City,
		API:          false,
	})
	s.enqueue(record)
	return true
}

// StartNginxLog tails the visit access log until the context ends. A missing
// file is not fatal: the deployment may not have installed the log yet.
func (s *Service) StartNginxLog(ctx context.Context) {
	if s == nil || !s.Enabled() || strings.TrimSpace(s.cfg.NginxLogFile) == "" {
		return
	}
	go func() {
		path := s.cfg.NginxLogFile
		// The first open starts at the end so a restart never replays history.
		// Every reopen after rotation, truncation or a late-created file reads
		// from the beginning of the new file.
		startAtEnd := true
		missing := 0
		for ctx.Err() == nil {
			if _, err := os.Stat(path); err != nil {
				if startAtEnd {
					startAtEnd = false
				}
				// A deployment may not have installed the log yet: retry every
				// minute but report it only every half hour to keep logs quiet.
				if missing%30 == 0 {
					log.Printf("visit_analytics_nginx_log_unavailable path=%s error=%v", path, err)
				}
				missing++
				if !sleepContext(ctx, 60*time.Second) {
					return
				}
				continue
			}
			missing = 0
			err := s.tailNginxLog(ctx, path, startAtEnd)
			startAtEnd = false
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.Printf("visit_analytics_nginx_log_failed path=%s error=%v", path, err)
				if !sleepContext(ctx, 10*time.Second) {
					return
				}
				continue
			}
			// Rotation or truncation was detected; reopen immediately.
			if !sleepContext(ctx, 100*time.Millisecond) {
				return
			}
		}
	}()
}

// tailNginxLog follows a log file and detects rotation or truncation.
func (s *Service) tailNginxLog(ctx context.Context, path string, startAtEnd bool) error {
	poll := time.Duration(s.cfg.NginxPollMillis) * time.Millisecond
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	handle, err := os.Open(path)
	if err != nil {
		return err
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil {
		return err
	}
	position := int64(0)
	if startAtEnd {
		if position, err = handle.Seek(0, io.SeekEnd); err != nil {
			return err
		}
	}
	reader := bufio.NewReaderSize(handle, 64<<10)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr == nil {
			position += int64(len(line))
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				s.ingestNginxLine(trimmed)
			}
			continue
		}
		if readErr != io.EOF {
			return readErr
		}
		// Keep an unterminated trailing line for the next read instead of
		// consuming half a record.
		if len(line) > 0 {
			if _, err = handle.Seek(position, io.SeekStart); err != nil {
				return err
			}
			reader.Reset(handle)
		}
		current, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !os.SameFile(opened, current) || current.Size() < position {
			// Rotated or truncated: the caller reopens and restarts.
			return nil
		}
		if !sleepContext(ctx, poll) {
			return nil
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
