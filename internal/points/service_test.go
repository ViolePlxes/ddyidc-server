package points

import (
	"testing"
	"time"
)

func TestCalculateModes(t *testing.T) {
	s := &Service{now: time.Now}
	tests := []struct {
		name   string
		rule   Rule
		amount int64
		want   int64
	}{
		{"fixed", Rule{Type: "fixed", PointsFixed: 25}, 0, 25},
		{"ratio", Rule{Type: "ratio", Ratio: 10}, 12500, 12},
		{"ladder", Rule{Type: "ladder", LadderConfig: []byte(`[{"min":10000,"max":19999,"points":80}]`)}, 15000, 80},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.calculate(&tt.rule, tt.amount); got != tt.want {
				t.Fatalf("calculate=%d want %d", got, tt.want)
			}
		})
	}
}

func TestPeriodKeys(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.Local)
	if got := periodKey("daily", now); got != "20260911" {
		t.Fatalf("daily=%s", got)
	}
	if got := periodKey("monthly", now); got != "202609" {
		t.Fatalf("monthly=%s", got)
	}
	if got := periodKey("once", now); got != "once" {
		t.Fatalf("once=%s", got)
	}
}
