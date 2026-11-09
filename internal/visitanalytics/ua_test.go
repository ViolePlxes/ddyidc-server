package visitanalytics

import "testing"

func TestParseClientBrowserMatrix(t *testing.T) {
	cases := []struct {
		name      string
		userAgent string
		browser   string
		os        string
		device    string
		kind      string
		bot       bool
		botName   string
	}{
		{
			name:      "chrome windows desktop",
			userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
			browser:   "Chrome", os: "Windows", device: "desktop", kind: KindBrowser,
		},
		{
			name:      "edge windows desktop",
			userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
			browser:   "Edge", os: "Windows", device: "desktop", kind: KindBrowser,
		},
		{
			name:      "safari macOS desktop",
			userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15",
			browser:   "Safari", os: "macOS", device: "desktop", kind: KindBrowser,
		},
		{
			name:      "firefox linux desktop",
			userAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0",
			browser:   "Firefox", os: "Linux", device: "desktop", kind: KindBrowser,
		},
		{
			name:      "chrome android mobile",
			userAgent: "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36",
			browser:   "Chrome", os: "Android", device: "mobile", kind: KindBrowser,
		},
		{
			name:      "safari ipad tablet",
			userAgent: "Mozilla/5.0 (iPad; CPU OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Mobile/15E148 Safari/604.1",
			browser:   "Safari", os: "iOS", device: "tablet", kind: KindBrowser,
		},
		{
			name:      "chromeos maps to linux",
			userAgent: "Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
			browser:   "Chrome", os: "Linux", device: "desktop", kind: KindBrowser,
		},
		{
			name:      "googlebot",
			userAgent: "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
			os:        labelOther, device: "desktop", kind: KindBot, bot: true, botName: "Googlebot", browser: "Googlebot",
		},
		{
			name:      "bingbot",
			userAgent: "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)",
			os:        labelOther, device: "desktop", kind: KindBot, bot: true, botName: "Bingbot", browser: "Bingbot",
		},
		{
			name:      "baiduspider",
			userAgent: "Mozilla/5.0 (compatible; Baiduspider/2.0; +http://www.baidu.com/search/spider.html)",
			os:        labelOther, device: "desktop", kind: KindBot, bot: true, botName: "Baiduspider", browser: "Baiduspider",
		},
		{
			name:      "ahrefsbot mobile",
			userAgent: "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36 (compatible; AhrefsBot/7.0)",
			os:        "Android", device: "mobile", kind: KindBot, bot: true, botName: "AhrefsBot", browser: "AhrefsBot",
		},
		{
			name:      "unknown crawler falls back to other",
			userAgent: "SomeRandomCrawler/1.0 (+http://example.invalid/crawler)",
			os:        labelOther, device: "desktop", kind: KindBot, bot: true, botName: "其他爬虫", browser: "其他爬虫",
		},
		{
			name:      "curl is api traffic",
			userAgent: "curl/8.5.0",
			browser:   "API", os: labelOther, device: "unknown", kind: KindAPI,
		},
		{
			name:      "empty user agent is api traffic",
			userAgent: "",
			browser:   "API", os: "unknown", device: "unknown", kind: KindAPI,
		},
		{
			name:      "go http client is api traffic",
			userAgent: "Go-http-client/1.1",
			browser:   "API", os: labelOther, device: "unknown", kind: KindAPI,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			client := ParseClient(testCase.userAgent)
			if client.Browser != testCase.browser {
				t.Fatalf("browser = %q, want %q", client.Browser, testCase.browser)
			}
			if client.OS != testCase.os {
				t.Fatalf("os = %q, want %q", client.OS, testCase.os)
			}
			if client.Device != testCase.device {
				t.Fatalf("device = %q, want %q", client.Device, testCase.device)
			}
			if client.Kind != testCase.kind {
				t.Fatalf("kind = %q, want %q", client.Kind, testCase.kind)
			}
			if client.IsBot != testCase.bot {
				t.Fatalf("isBot = %v, want %v", client.IsBot, testCase.bot)
			}
			if testCase.botName != "" && client.BotName != testCase.botName {
				t.Fatalf("botName = %q, want %q", client.BotName, testCase.botName)
			}
			// The raw header must never survive parsing.
			if len(testCase.userAgent) > 0 && client.Browser == testCase.userAgent {
				t.Fatal("raw user agent leaked into a stored label")
			}
		})
	}
}

func TestParseClientNeverStoresRawAgent(t *testing.T) {
	raw := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/131.0.0.0 Safari/537.36 unique-marker-7f21"
	client := ParseClient(raw)
	for _, value := range []string{client.Browser, client.OS, client.Device, client.BotName} {
		if value != "" && len(value) >= len(raw) {
			t.Fatalf("label %q is as long as the raw header", value)
		}
	}
}
