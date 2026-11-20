package certification

import (
	"reflect"
	"testing"
)

// The stored configuration must keep working for installs that still carry the
// single legacy channel value, while new installs list every allowed channel.
func TestAllowedChannels(t *testing.T) {
	cases := []struct {
		name     string
		settings Settings
		want     []string
	}{
		{"legacy-empty", Settings{}, []string{"manual"}},
		{"legacy-manual", Settings{Channel: "manual"}, []string{"manual"}},
		{"legacy-alipay", Settings{Channel: "alipay"}, []string{"alipay"}},
		{"legacy-tencent", Settings{Channel: "tencent"}, []string{"tencent"}},
		{"both-auto", Settings{Channels: []string{"tencent", "alipay"}}, []string{"alipay", "tencent"}},
		{"all-three", Settings{Channels: []string{"tencent", "manual", "alipay"}}, []string{"manual", "alipay", "tencent"}},
		{"list-wins-over-legacy", Settings{Channel: "manual", Channels: []string{"alipay"}}, []string{"alipay"}},
		{"unknown-only-falls-back", Settings{Channels: []string{"wechat"}}, []string{"manual"}},
		{"normalised-irrelevant-input", Settings{Channels: []string{"alipay", "alipay"}}, []string{"alipay"}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := item.settings.allowedChannels(); !reflect.DeepEqual(got, item.want) {
				t.Fatalf("allowedChannels = %v, want %v", got, item.want)
			}
			for _, channel := range item.want {
				if !item.settings.Allows(channel) {
					t.Fatalf("%s must be allowed", channel)
				}
			}
			for _, channel := range certificationChannels {
				allowed := false
				for _, value := range item.want {
					if value == channel {
						allowed = true
					}
				}
				if item.settings.Allows(channel) != allowed {
					t.Fatalf("Allows(%s) mismatch", channel)
				}
			}
		})
	}
}

func TestSelectedChannelsValidation(t *testing.T) {
	cases := []struct {
		name     string
		settings Settings
		want     []string
		fails    bool
	}{
		{"customer-choice", Settings{Channels: []string{"alipay", "tencent"}}, []string{"alipay", "tencent"}, false},
		{"dedupe-and-order", Settings{Channels: []string{"tencent", "manual", "tencent"}}, []string{"manual", "tencent"}, false},
		{"legacy-single", Settings{Channel: "alipay"}, []string{"alipay"}, false},
		{"empty-becomes-manual", Settings{}, []string{"manual"}, false},
		{"unknown-channel", Settings{Channels: []string{"alipay", "wechat"}}, nil, true},
		{"legacy-unknown", Settings{Channel: "wechat"}, nil, true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got, err := selectedChannels(item.settings)
			if item.fails {
				if err == nil {
					t.Fatalf("invalid selection accepted: %v", got)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, item.want) {
				t.Fatalf("selectedChannels = %v (%v), want %v", got, err, item.want)
			}
		})
	}
}
