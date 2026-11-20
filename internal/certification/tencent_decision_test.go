package certification

import "testing"

func tencentPassText() tencentText {
	return tencentText{ErrCode: 0, LiveStatus: 0, Comparestatus: 0, Sim: 95.51, LivenessDetail: []tencentLiveness{{Seq: "1"}, {Seq: "2"}}}
}

func TestTencentDecisionMatrix(t *testing.T) {
	pass := tencentPassText()
	if decision := decideTencent(pass, 70); !decision.Passed || decision.Reason != "" {
		t.Fatalf("clean result rejected: %+v", decision)
	}
	// Every published condition is mandatory.
	cases := []struct {
		name   string
		mutate func(*tencentText)
		reason string
	}{
		{"provider_error", func(text *tencentText) { text.ErrCode = 1001 }, "provider_error"},
		{"liveness_failed", func(text *tencentText) { text.LiveStatus = 1 }, "liveness_failed"},
		{"compare_failed", func(text *tencentText) { text.Comparestatus = 1 }, "compare_failed"},
		{"similarity_below", func(text *tencentText) { text.Sim = 69.99 }, "similarity_below_threshold"},
		{"liveness_detail_err", func(text *tencentText) { text.LivenessDetail[1].Errcode = 3 }, "liveness_detail_failed"},
		{"liveness_detail_status", func(text *tencentText) { text.LivenessDetail[0].Livestatus = 1 }, "liveness_detail_failed"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			text := tencentPassText()
			item.mutate(&text)
			decision := decideTencent(text, 70)
			if decision.Passed || decision.Reason != item.reason {
				t.Fatalf("decision = %+v", decision)
			}
		})
	}
	// Boundary: the threshold is inclusive.
	text := tencentPassText()
	text.Sim = 70
	if decision := decideTencent(text, 70); !decision.Passed {
		t.Fatal("Sim equal to the threshold must pass")
	}
	// No liveness detail at all must not silently pass a failed live status.
	text = tencentPassText()
	text.LivenessDetail = nil
	text.LiveStatus = 2
	if decision := decideTencent(text, 70); decision.Passed {
		t.Fatal("failed live status accepted without details")
	}
	// Missing Sim (empty string) is a failure, not a pass.
	text = tencentPassText()
	text.Sim = 0
	if decision := decideTencent(text, 70); decision.Passed || decision.Reason != "similarity_below_threshold" {
		t.Fatalf("missing Sim = %+v", decision)
	}
}
