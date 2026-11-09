package visitanalytics

import "strings"

// Client is the parsed, non-identifying view of a User-Agent header. The raw
// header is never persisted; only these coarse labels are stored.
type Client struct {
	Browser string
	OS      string
	Device  string
	IsBot   bool
	BotName string
	Kind    string
}

// Client kinds match the stored client_type column and the query whitelist.
const (
	KindBrowser = "browser"
	KindBot     = "bot"
	KindAPI     = "api"
)

// Labels are display values stored verbatim, so they are part of the data
// contract and must stay stable.
const (
	labelOther = "其他"
)

type botSignature struct{ token, name string }

// Ordered most specific first: Applebot must win over the generic "bot" rule,
// and Googlebot-Image over Googlebot.
var botSignatures = []botSignature{
	{"googlebot-image", "Googlebot"},
	{"googlebot-news", "Googlebot"},
	{"googlebot-video", "Googlebot"},
	{"googlebot", "Googlebot"},
	{"bingbot", "Bingbot"},
	{"bingpreview", "Bingbot"},
	{"baiduspider", "Baiduspider"},
	{"ahrefsbot", "AhrefsBot"},
	{"semrushbot", "SemrushBot"},
	{"yandexbot", "YandexBot"},
	{"mj12bot", "MJ12Bot"},
	{"dotbot", "DotBot"},
	{"petalbot", "PetalBot"},
	{"bytespider", "Bytespider"},
	{"applebot", "Applebot"},
	{"duckduckbot", "DuckDuckBot"},
	{"gptbot", "GPTBot"},
	{"chatgpt-user", "ChatGPT-User"},
	{"claudebot", "ClaudeBot"},
	{"ccbot", "CCBot"},
	{"amazonbot", "Amazonbot"},
	{"facebookexternalhit", "FacebookBot"},
	{"telegrambot", "TelegramBot"},
	{"twitterbot", "Twitterbot"},
	{"slackbot", "SlackBot"},
	{"discordbot", "DiscordBot"},
	{"linkedinbot", "LinkedInBot"},
	{"sogou web spider", "SogouSpider"},
	{"haosouspider", "360Spider"},
	{"360spider", "360Spider"},
	{"exabot", "Exabot"},
	{"imagesiftbot", "ImagesiftBot"},
	{"scrapy", "Scrapy"},
	{"headlesschrome", "其他爬虫"},
}

// Non-browser HTTP clients are recorded as API traffic so page samples are not
// polluted by monitoring probes and SDK calls.
var apiSignatures = []string{
	"curl/", "wget/", "python-requests", "python-urllib", "aiohttp", "httpx/",
	"go-http-client", "okhttp", "java/", "apache-httpclient", "node-fetch",
	"axios/", "libwww-perl", "httpclient", "postmanruntime", "insomnia",
	"powershell", "dart/", "grpc-", "http.rb", "httpie", "deno/", "bun/",
}

type browserSignature struct{ token, name string }

var browserSignatures = []browserSignature{
	{"edg/", "Edge"},
	{"edge/", "Edge"},
	{"opr/", "Opera"},
	{"opera", "Opera"},
	{"chrome/", "Chrome"},
	{"crios/", "Chrome"},
	{"chromium", "Chrome"},
	{"firefox/", "Firefox"},
	{"fxios/", "Firefox"},
	{"safari/", "Safari"},
}

// ParseClient derives coarse client labels from a User-Agent value. It performs
// no I/O and never returns any substring of the input.
func ParseClient(userAgent string) Client {
	ua := strings.ToLower(strings.TrimSpace(userAgent))
	client := Client{Browser: labelOther, OS: labelOther, Device: "desktop", Kind: KindBrowser}
	if ua == "" {
		client.Kind = KindAPI
		client.Browser = "API"
		client.OS = "unknown"
		client.Device = "unknown"
		return client
	}
	for _, signature := range botSignatures {
		if strings.Contains(ua, signature.token) {
			client.IsBot = true
			client.BotName = signature.name
			client.Browser = signature.name
			client.Kind = KindBot
			client.OS = parseOS(ua)
			client.Device = parseDevice(ua)
			return client
		}
	}
	if strings.Contains(ua, "spider") || strings.Contains(ua, "crawler") || strings.Contains(ua, "bot/") || strings.HasSuffix(ua, "bot") {
		client.IsBot = true
		client.BotName = "其他爬虫"
		client.Browser = client.BotName
		client.Kind = KindBot
		client.OS = parseOS(ua)
		client.Device = parseDevice(ua)
		return client
	}
	for _, signature := range apiSignatures {
		if strings.Contains(ua, signature) {
			client.Kind = KindAPI
			client.Browser = "API"
			client.OS = parseOS(ua)
			client.Device = "unknown"
			return client
		}
	}
	for _, signature := range browserSignatures {
		if strings.Contains(ua, signature.token) {
			client.Browser = signature.name
			break
		}
	}
	client.OS = parseOS(ua)
	client.Device = parseDevice(ua)
	return client
}

func parseOS(ua string) string {
	switch {
	case strings.Contains(ua, "iphone"), strings.Contains(ua, "ipad"), strings.Contains(ua, "ipod"):
		return "iOS"
	case strings.Contains(ua, "android"):
		return "Android"
	case strings.Contains(ua, "windows nt"), strings.Contains(ua, "windows phone"):
		return "Windows"
	case strings.Contains(ua, "mac os x"), strings.Contains(ua, "macintosh"):
		return "macOS"
	// ChromeOS and desktop Linux share the Linux label so the stored OS list
	// stays exactly the documented Windows/macOS/Linux/iOS/Android set.
	case strings.Contains(ua, "cros"), strings.Contains(ua, "linux"), strings.Contains(ua, "x11"):
		return "Linux"
	default:
		return labelOther
	}
}

func parseDevice(ua string) string {
	switch {
	case strings.Contains(ua, "ipad"), strings.Contains(ua, "tablet"), strings.Contains(ua, "playbook"), strings.Contains(ua, "silk/"), strings.Contains(ua, "kindle"):
		return "tablet"
	case strings.Contains(ua, "android") && !strings.Contains(ua, "mobile"):
		return "tablet"
	case strings.Contains(ua, "mobile"), strings.Contains(ua, "iphone"), strings.Contains(ua, "ipod"), strings.Contains(ua, "windows phone"), strings.Contains(ua, "blackberry"), strings.Contains(ua, "opera mini"), strings.Contains(ua, "iemobile"):
		return "mobile"
	default:
		return "desktop"
	}
}
