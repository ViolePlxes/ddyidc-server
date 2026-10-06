package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/smtp"
	"time"
)

func (h *adminHandler) digestDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	config, e := h.manager.loadConfig(r.Context())
	if e != nil {
		adminError(w, 500, 500, "读取通知配置失败")
		return
	}
	if h.manager.mockMode(config, ChannelEmail, "expiry_digest") {
		adminError(w, 400, 400, "邮件配置未完成")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	port := config.Mail.Port
	if port == 0 {
		port = 465
	}
	address := net.JoinHostPort(config.Mail.Host, fmt.Sprint(port))
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tlsConfig := &tls.Config{ServerName: config.Mail.Host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	if port == 465 {
		conn, e = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		conn, e = dialer.DialContext(ctx, "tcp", address)
	}
	if e != nil {
		adminError(w, 502, 502, "SMTP DNS、连接或 TLS 校验失败；未发送邮件")
		return
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	client, e := smtp.NewClient(conn, config.Mail.Host)
	if e != nil {
		adminError(w, 502, 502, "SMTP 握手失败；未发送邮件")
		return
	}
	defer client.Close()
	if port != 465 {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			adminError(w, 502, 502, "SMTP 未提供 STARTTLS；未发送邮件")
			return
		}
		if e = client.StartTLS(tlsConfig); e != nil {
			adminError(w, 502, 502, "SMTP TLS 校验失败；未发送邮件")
			return
		}
	}
	if e = client.Auth(smtp.PlainAuth("", config.Mail.Username, config.Mail.Password, config.Mail.Host)); e != nil {
		adminError(w, 502, 502, "SMTP 认证失败；未发送邮件")
		return
	}
	_ = client.Quit()
	adminOK(w, map[string]any{"dns": true, "connection": true, "tls": true, "authentication": true, "sent": false, "checkedAt": time.Now().UTC()})
}
