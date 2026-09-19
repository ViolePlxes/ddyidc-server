package videobind

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Adapter is the extension boundary for verified platform-specific protocols.
// Unsupported providers fail closed instead of treating a self-reported UID as proof.
type Adapter interface {
	CheckFollow(context.Context, *Platform, *Binding) (bool, error)
}
type LimitedAdapter struct{}

func (LimitedAdapter) CheckFollow(context.Context, *Platform, *Binding) (bool, error) {
	return false, ErrLimited
}
func VerifyDouyin(secret string, body []byte, signature string) bool {
	if secret == "" {
		return false
	}
	actual, e := hex.DecodeString(signature)
	if e != nil {
		return false
	}
	sum := sha1.Sum(append([]byte(secret), body...))
	return subtle.ConstantTimeCompare(actual, sum[:]) == 1
}

type douyinEvent struct {
	Event     string          `json:"event"`
	ClientKey string          `json:"client_key"`
	From      string          `json:"from_user_id"`
	To        string          `json:"to_user_id"`
	Content   json.RawMessage `json:"content"`
}

func contentObject(raw json.RawMessage) json.RawMessage {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return json.RawMessage(text)
	}
	return raw
}
func (s *Service) Callback(ctx context.Context, code string, body []byte, signature string) (any, error) {
	p, e := s.platform(ctx, code, true)
	if e != nil {
		return nil, e
	}
	if code != "douyin" || p.Mode != "webhook" {
		return nil, ErrDisabled
	}
	c, e := s.credentials(p)
	if e != nil {
		return nil, e
	}
	if c.Secret == "" || c.AppID == "" {
		return nil, ErrDisabled
	}
	if !VerifyDouyin(c.Secret, body, signature) {
		return nil, errors.New("回调验签失败")
	}
	var event douyinEvent
	if json.Unmarshal(body, &event) != nil || event.ClientKey != c.AppID {
		return nil, errors.New("回调应用不匹配")
	}
	if event.Event == "verify_webhook" {
		var challenge struct {
			Challenge json.RawMessage `json:"challenge"`
		}
		if json.Unmarshal(contentObject(event.Content), &challenge) != nil || len(challenge.Challenge) == 0 || len(challenge.Challenge) > 256 {
			return nil, errors.New("challenge 格式错误")
		}
		return map[string]any{"challenge": challenge.Challenge}, nil
	}
	if event.Event != "new_follow_action" && event.Event != "unauthorize" {
		return map[string]int{"code": 0}, nil
	}
	if !validID(event.From) {
		return nil, errors.New("事件用户无效")
	}
	var b Binding
	if e = s.db.WithContext(ctx).Where("platform=? AND identity_hash=? AND unbound_at IS NULL", code, s.hash(code, event.From)).Take(&b).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return map[string]int{"code": 0}, nil
		}
		return nil, errors.New("事件处理暂不可用")
	}
	if event.Event == "unauthorize" {
		return map[string]int{"code": 0}, s.db.WithContext(ctx).Model(&Binding{}).Where("id=?", b.ID).Updates(map[string]any{"unbound_at": time.Now(), "status": "unfollowed", "verified": false, "updated_at": time.Now()}).Error
	}
	if c.OfficialID == "" || event.To != c.OfficialID {
		return nil, errors.New("关注目标非配置的官方账号")
	}
	var action struct {
		Type *int  `json:"action_type"`
		Time int64 `json:"action_time"`
	}
	if json.Unmarshal(contentObject(event.Content), &action) != nil || (action.Type == nil || *action.Type != 1) {
		return nil, errors.New("关注事件无效")
	}
	now := time.Now().Unix()
	if action.Time < now-600 || action.Time > now+60 {
		return nil, errors.New("关注事件已过期")
	}
	_, e = s.follow(ctx, b.ID, action.Time, *action.Type == 1, 0, "")
	return map[string]int{"code": 0}, e
}

type oauthState struct {
	User     int64
	AppID    string
	Redirect string
}

func (s *Service) Authorize(ctx context.Context, user int64, platform string) (string, error) {
	p, e := s.platform(ctx, platform, true)
	if e != nil {
		return "", e
	}
	if platform != "douyin" || p.Mode != "webhook" {
		return "", ErrLimited
	}
	c, e := s.credentials(p)
	if e != nil {
		return "", e
	}
	if c.AppID == "" || c.Secret == "" || !validURL(p.CallbackURL) {
		return "", ErrDisabled
	}
	if s.redis == nil {
		return "", errors.New("授权状态服务不可用")
	}
	raw := make([]byte, 32)
	if _, e = rand.Read(raw); e != nil {
		return "", e
	}
	state := hex.EncodeToString(raw)
	redirect := strings.Replace(p.CallbackURL, "/bind/callback/", "/oauth/callback/", 1)
	encoded, _ := json.Marshal(oauthState{user, c.AppID, redirect})
	if e = s.redis.Set(ctx, "video:oauth:"+state, encoded, 10*time.Minute).Err(); e != nil {
		return "", errors.New("授权状态服务不可用")
	}
	q := url.Values{"client_key": {c.AppID}, "response_type": {"code"}, "scope": {c.Scope}, "redirect_uri": {redirect}, "state": {state}}
	return "https://open.douyin.com/platform/oauth/connect/?" + q.Encode(), nil
}
func (s *Service) CompleteOAuth(ctx context.Context, browserState, platform, state, code string) error {
	if len(browserState) != 64 || subtle.ConstantTimeCompare([]byte(browserState), []byte(state)) != 1 || platform != "douyin" || len(state) != 64 || len(code) == 0 || len(code) > 2048 || s.redis == nil {
		return errors.New("授权参数无效")
	}
	raw, e := s.redis.GetDel(ctx, "video:oauth:"+state).Result()
	if e != nil {
		return errors.New("授权已过期或已使用")
	}
	var st oauthState
	if json.Unmarshal([]byte(raw), &st) != nil || st.User <= 0 {
		return errors.New("授权状态无效")
	}
	p, e := s.platform(ctx, platform, true)
	if e != nil {
		return e
	}
	c, e := s.credentials(p)
	if e != nil {
		return e
	}
	if c.AppID != st.AppID || p.Mode != "webhook" {
		return errors.New("应用配置已变更，请重新授权")
	}
	form := url.Values{"client_key": {c.AppID}, "client_secret": {c.Secret}, "code": {code}, "grant_type": {"authorization_code"}}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, "https://open.douyin.com/oauth/access_token/", strings.NewReader(form.Encode()))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("抖音授权服务暂不可用，请重新发起")
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			OpenID    string `json:"open_id"`
			ErrorCode int    `json:"error_code"`
		} `json:"data"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result) != nil || result.Data.ErrorCode != 0 || !validID(result.Data.OpenID) {
		return errors.New("抖音授权未通过，请检查应用权限或重新授权")
	}
	// Tokens are intentionally discarded: this adapter only requires the verified open_id.
	_, e = s.Bind(ctx, st.User, platform, result.Data.OpenID, "", true)
	return e
}
func (s *Service) rate(ctx context.Context, key string, limit int64) error {
	if s.redis == nil {
		return errors.New("频控服务不可用")
	}
	n, e := s.redis.Eval(ctx, `local n=redis.call('INCR',KEYS[1]);if n==1 then redis.call('EXPIRE',KEYS[1],60) end;return n`, []string{"video:rate:" + key}).Int64()
	if e != nil {
		return errors.New("频控服务不可用")
	}
	if n > limit {
		return fmt.Errorf("操作过于频繁，请稍后重试（%s）", strconv.FormatInt(limit, 10))
	}
	return nil
}
