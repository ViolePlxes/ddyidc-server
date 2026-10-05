package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeNotifyRepository struct {
	claims    map[string]bool
	values    map[string]string
	templates map[string]*NotificationTemplate
	logs      []NotifyLog
	contact   Contact
	expiring  []ExpiringInstance
}

func (f *fakeNotifyRepository) ClaimExpiry(_ context.Context, business string, _ int64, _ int64, _ string, _ string) (bool, error) {
	if f.claims == nil {
		f.claims = map[string]bool{}
	}
	if f.claims[business] {
		return false, nil
	}
	f.claims[business] = true
	return true, nil
}
func (f *fakeNotifyRepository) FinishExpiry(context.Context, string, int64, int64, string, string) error {
	return nil
}

func (f *fakeNotifyRepository) EnsureSchema(context.Context) error { return nil }
func (f *fakeNotifyRepository) ConfigValues(context.Context) (map[string]string, error) {
	return cloneMap(f.values), nil
}
func (f *fakeNotifyRepository) SaveConfig(_ context.Context, key, value string) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return nil
}
func (f *fakeNotifyRepository) TemplateByScene(_ context.Context, scene string) (*NotificationTemplate, error) {
	return f.templates[scene], nil
}
func (f *fakeNotifyRepository) ListTemplates(context.Context) ([]NotificationTemplate, error) {
	return nil, nil
}
func (f *fakeNotifyRepository) UpdateTemplate(context.Context, int64, string, string, string, bool) error {
	return nil
}
func (f *fakeNotifyRepository) CreateLog(_ context.Context, logEntry *NotifyLog) error {
	f.logs = append(f.logs, *logEntry)
	return nil
}
func (f *fakeNotifyRepository) ListLogs(context.Context, int, int, string, string, string) ([]NotifyLog, int64, error) {
	return f.logs, int64(len(f.logs)), nil
}
func (f *fakeNotifyRepository) CustomerContact(context.Context, int64) (Contact, error) {
	return f.contact, nil
}
func (f *fakeNotifyRepository) AdminContact(context.Context, int64) (Contact, error) {
	return f.contact, nil
}
func (f *fakeNotifyRepository) ExpiringInstances(context.Context, time.Time) ([]ExpiringInstance, error) {
	return f.expiring, nil
}
func (f *fakeNotifyRepository) HasSuccessfulLog(_ context.Context, scene, businessID string) (bool, error) {
	for _, item := range f.logs {
		if item.Scene == scene && item.BusinessID == businessID && item.Status == StatusSuccess {
			return true, nil
		}
	}
	return false, nil
}

type fakeSMSChannel struct{ err error }

func (f fakeSMSChannel) Send(context.Context, SMSConfig, string, string, map[string]string) (string, error) {
	return "biz-test", f.err
}

type fakeMailChannel struct {
	err   error
	calls int
}

func (f *fakeMailChannel) Send(context.Context, MailConfig, string, string, string) error {
	f.calls++
	return f.err
}

func testManager(repo *fakeNotifyRepository, mail mailChannel) *Manager {
	return &Manager{
		repo:   repo,
		yaml:   Config{Mail: MailConfig{Host: "smtp.example.com", Port: 587, Username: "sender@example.com", Password: "secret", FromName: "DDYIDC"}, SMS: SMSConfig{AccessKey: "test-key", AccessSecret: "test-secret", SignName: "DDYIDC", Templates: map[string]string{"register": "SMS_1"}}},
		aesKey: []byte("0123456789abcdef0123456789abcdef"), sms: fakeSMSChannel{}, mail: mail, retryDelay: 0, now: time.Now,
	}
}

func TestRenderTemplate(t *testing.T) {
	rendered, err := RenderTemplate("你好 {{name}}，验证码 {{code}}", map[string]string{"name": "张三", "code": "123456"}, false)
	if err != nil || rendered != "你好 张三，验证码 123456" {
		t.Fatalf("unexpected render: %q %v", rendered, err)
	}
	if _, err = RenderTemplate("{{missing}}", map[string]string{}, false); err == nil {
		t.Fatal("expected missing variable error")
	}
	rendered, err = RenderTemplate("用户：{{name}}", map[string]string{"name": `<script>alert("x")</script>`}, true)
	if err != nil || strings.Contains(rendered, "<script>") || !strings.Contains(rendered, "&lt;script&gt;") {
		t.Fatalf("HTML value was not escaped: %q %v", rendered, err)
	}
}

func TestMockModeDependsOnCompleteCredentials(t *testing.T) {
	manager := &Manager{}
	config := RuntimeConfig{}
	if !manager.mockMode(config, ChannelEmail, "register") || !manager.mockMode(config, ChannelSMS, "register") {
		t.Fatal("missing credentials must use mock")
	}
	config.Mail = MailConfig{Host: "smtp.example.com", Port: 465, Username: "u", Password: "p"}
	config.SMS = SMSConfig{AccessKey: "ak", AccessSecret: "sk", SignName: "sign", Templates: map[string]string{"register": "SMS_1"}}
	if manager.mockMode(config, ChannelEmail, "register") || manager.mockMode(config, ChannelSMS, "register") {
		t.Fatal("complete credentials must disable mock")
	}
}

func TestSaveConfigEncryptsCredentialsAndDisablesMock(t *testing.T) {
	repo := &fakeNotifyRepository{values: map[string]string{}, templates: map[string]*NotificationTemplate{}}
	manager := testManager(repo, &fakeMailChannel{})
	host, username, password, fromName := "smtp.example.com", "sender@example.com", "mail-secret", "DDYIDC"
	port := 587
	accessKey, accessSecret, signName := "LTAI1234567890", "aliyun-secret", "DDYIDC"
	input := ConfigInput{
		Mail:   MailConfigInput{Host: &host, Port: &port, Username: &username, Password: &password, FromName: &fromName},
		SMS:    SMSConfigInput{AccessKey: &accessKey, AccessSecret: &accessSecret, SignName: &signName, Templates: map[string]string{"register": "SMS_123"}},
		Expiry: &ExpiryConfig{Enabled: true, DaysBefore: 3},
	}
	if err := manager.SaveConfig(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"notify.mail.username", "notify.mail.password", "notify.sms.access_key", "notify.sms.access_secret"} {
		if !strings.HasPrefix(repo.values[key], encryptedPrefix) || strings.Contains(repo.values[key], "secret") {
			t.Fatalf("credential %s was not encrypted: %q", key, repo.values[key])
		}
	}
	loaded, err := manager.loadConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if manager.mockMode(loaded, ChannelEmail, "register") || manager.mockMode(loaded, ChannelSMS, "register") {
		t.Fatal("complete stored credentials did not disable mock")
	}
	view, err := manager.Config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Mail.Password != "********" || view.SMS.AccessSecret != "********" || !strings.Contains(view.SMS.AccessKey, "****") {
		t.Fatalf("credentials were not masked: %#v", view)
	}
}

func TestLegacyRPCSignatureGolden(t *testing.T) {
	values := url.Values{
		"AccessKeyId": {"testid"}, "Action": {"DescribeRegions"}, "Format": {"XML"},
		"SignatureMethod": {"HMAC-SHA1"}, "SignatureNonce": {"3ee8c1b8-83d3-44af-a94f-4e0ad82fd6cf"}, "SignatureVersion": {"1.0"},
		"Timestamp": {"2016-02-23T12:46:24Z"}, "Version": {"2014-05-26"},
	}
	if got := legacyRPCSignature("GET", "testsecret", values); got != "OLeaidS1JvxuMvnyHOwuJ+uX5qY=" {
		t.Fatalf("golden signature mismatch: %s", got)
	}
}

func TestFailedDeliveryIsLogged(t *testing.T) {
	repo := &fakeNotifyRepository{values: map[string]string{}, templates: map[string]*NotificationTemplate{"order": {ID: 1, Scene: "order", Channel: ChannelEmail, Title: "订单", Content: "订单 {{order_no}}", Enabled: true}}}
	mail := &fakeMailChannel{err: errors.New("SMTP unavailable")}
	manager := testManager(repo, mail)
	err := manager.SendNotify(context.Background(), "order", "user@example.com", map[string]string{"order_no": "DDY1"})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if mail.calls != 2 {
		t.Fatalf("expected one retry, got %d calls", mail.calls)
	}
	if len(repo.logs) != 1 || repo.logs[0].Status != StatusFailed || !strings.Contains(repo.logs[0].Error, "邮件服务发送失败") {
		t.Fatalf("failed log missing: %#v", repo.logs)
	}
}

func TestAdminTestDeliveryDoesNotRetry(t *testing.T) {
	repo := &fakeNotifyRepository{values: map[string]string{}, templates: map[string]*NotificationTemplate{"register": {ID: 3, Scene: "register", Channel: ChannelEmail, Title: "验证码", Content: "验证码 {{code}}", Enabled: true}}}
	mail := &fakeMailChannel{err: errors.New("delivery outcome unknown Authorization: test-sensitive-password")}
	manager := testManager(repo, mail)
	err := manager.SendTestNotify(context.Background(), "register", "user@example.com", map[string]string{"code": "123456"})
	if err == nil || mail.calls != 1 || len(repo.logs) != 1 || repo.logs[0].Status != StatusFailed {
		t.Fatal("test delivery must make exactly one attempt and record failure")
	}
	if strings.Contains(repo.logs[0].ContentSnapshot, "123456") {
		t.Fatal("verification code leaked into notification log")
	}
	if strings.Contains(err.Error(), "test-sensitive-password") || strings.Contains(repo.logs[0].Error, "Authorization") {
		t.Fatal("raw provider error leaked to response or audit log")
	}
}

func TestVerificationProviderFailureReturnsBadGateway(t *testing.T) {
	repo := &fakeNotifyRepository{values: map[string]string{}, templates: map[string]*NotificationTemplate{"register": {ID: 3, Scene: "register", Channel: ChannelEmail, Title: "验证码", Content: "验证码 {{code}}，{{minutes}} 分钟有效", Enabled: true}}}
	mail := &fakeMailChannel{err: errors.New("authentication failed")}
	manager := testManager(repo, mail)
	service := &verificationService{manager: manager, memory: map[string]memoryEntry{}}
	body, _ := json.Marshal(SendRequest{Email: "failure@example.com", Scene: "register"})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/notify/email/send", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	service.sendEmail(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response apiResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Code != 502 || !strings.Contains(response.Message, "发送失败") {
		t.Fatalf("response = %#v, %v", response, err)
	}
	if len(repo.logs) != 1 || repo.logs[0].Status != StatusFailed {
		t.Fatalf("failed delivery was not logged: %#v", repo.logs)
	}
}

func TestConsumeSMSCodeRejectsReplayAndReportsStates(t *testing.T) {
	service := &VerificationService{memory: map[string]memoryEntry{}}
	mobile := "13900001047"
	base := service.key(ChannelSMS, mobile, "register")
	encoded, _ := json.Marshal(record{Code: "654321", Expires: time.Now().Add(5 * time.Minute)})
	service.set(base+":code", string(encoded), 5*time.Minute)
	service.set(base+":sent", "1", 10*time.Minute)

	if err := service.ConsumeSMSCode(context.Background(), mobile, "register", "000000"); err == nil || err.Error() != "验证码错误" {
		t.Fatalf("wrong code error = %v", err)
	}
	if err := service.ConsumeSMSCode(context.Background(), mobile, "register", "654321"); err != nil {
		t.Fatalf("valid code failed: %v", err)
	}
	if err := service.ConsumeSMSCode(context.Background(), mobile, "register", "654321"); err == nil || err.Error() != "验证码已使用，请重新获取" {
		t.Fatalf("replay error = %v", err)
	}

	unsent := "13800000000"
	if err := service.ConsumeSMSCode(context.Background(), unsent, "register", "654321"); err == nil || err.Error() != "请先获取验证码" {
		t.Fatalf("unsent error = %v", err)
	}
	expiredBase := service.key(ChannelSMS, unsent, "register")
	service.set(expiredBase+":sent", "1", 10*time.Minute)
	if err := service.ConsumeSMSCode(context.Background(), unsent, "register", "654321"); err == nil || err.Error() != "验证码已过期，请重新获取" {
		t.Fatalf("expired error = %v", err)
	}
}

func TestExpiryScanSendsOnlyOnce(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour)
	repo := &fakeNotifyRepository{
		values:    map[string]string{"notify.expiry.enabled": "true", "notify.expiry.days_before": "3"},
		templates: map[string]*NotificationTemplate{"expiry": {ID: 2, Scene: "expiry", Channel: ChannelEmail, Title: "到期提醒", Content: "实例 {{instance_no}} 于 {{expires_at}} 到期", Enabled: true}},
		contact:   Contact{Email: "user@example.com"},
		expiring:  []ExpiringInstance{{ID: 99, CustomerID: 7, InstanceNo: "INS99", ProductName: "云主机", ExpiresAt: &expires}},
	}
	mail := &fakeMailChannel{}
	manager := testManager(repo, mail)
	first, err := manager.ScanExpiring(context.Background())
	if err != nil || first != 1 {
		t.Fatalf("first scan = %d, %v", first, err)
	}
	second, err := manager.ScanExpiring(context.Background())
	if err != nil || second != 0 {
		t.Fatalf("second scan = %d, %v", second, err)
	}
	if mail.calls != 1 {
		t.Fatalf("expected one delivery, got %d", mail.calls)
	}
	expires = expires.Add(24 * time.Hour)
	third, err := manager.ScanExpiring(context.Background())
	if err != nil || third != 1 || mail.calls != 2 {
		t.Fatalf("renewed expiry must have its own reminder: %d %v calls=%d", third, err, mail.calls)
	}
}

func TestExpiryScanWithoutEmailDoesNotDeliverOrRecordSuccess(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour)
	repo := &fakeNotifyRepository{
		values:    map[string]string{"notify.expiry.enabled": "true"},
		templates: map[string]*NotificationTemplate{"expiry": {ID: 2, Scene: "expiry", Channel: ChannelEmail, Title: "到期提醒", Content: "实例 {{instance_no}}", Enabled: true}},
		contact:   Contact{Phone: "13800000000"},
		expiring:  []ExpiringInstance{{ID: 99, CustomerID: 7, InstanceNo: "INS99", ExpiresAt: &expires}},
	}
	mail := &fakeMailChannel{}
	manager := testManager(repo, mail)
	for i := 0; i < 2; i++ {
		count, err := manager.ScanExpiring(context.Background())
		if err != nil || count != 0 || mail.calls != 0 || len(repo.logs) != 0 {
			t.Fatalf("unreachable reminder: count=%d err=%v calls=%d logs=%d", count, err, mail.calls, len(repo.logs))
		}
	}
	if err := manager.NotifyCustomer(context.Background(), "expiry", 7, "99", map[string]string{"instance_no": "INS99"}); !errors.Is(err, errUnavailableContact) {
		t.Fatalf("direct notification must still report unavailable contact: %v", err)
	}
}

func TestExpiryFailureIsNotResent(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	repo := &fakeNotifyRepository{values: map[string]string{"notify.expiry.enabled": "true"}, templates: map[string]*NotificationTemplate{"expiry": {ID: 2, Scene: "expiry", Channel: ChannelEmail, Title: "expiry", Content: "{{instance_no}}", Enabled: true}}, contact: Contact{Email: "fixture@example.com"}, expiring: []ExpiringInstance{{ID: 99, CustomerID: 7, InstanceNo: "fixture", ExpiresAt: &expires}}}
	mail := &fakeMailChannel{err: context.DeadlineExceeded}
	manager := testManager(repo, mail)
	first, err := manager.ScanExpiring(context.Background())
	if err != nil || first != 0 || mail.calls != 1 {
		t.Fatal("uncertain provider result retried", first, err, mail.calls)
	}
	second, err := manager.ScanExpiring(context.Background())
	if err != nil || second != 0 || mail.calls != 1 {
		t.Fatal("next scanner resent uncertain notification", second, err, mail.calls)
	}
	if len(repo.logs) != 1 || repo.logs[0].Status != StatusFailed {
		t.Fatal("failure evidence missing")
	}
	sent, err := manager.RemindExpiring(context.Background(), 99, 1, "127.0.0.1", "verified fixture")
	if err != nil || sent || mail.calls != 1 {
		t.Fatal("manual trigger resent uncertain notification")
	}
}
