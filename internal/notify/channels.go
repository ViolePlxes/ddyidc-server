package notify

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/utils"
	dysms "github.com/alibabacloud-go/dysmsapi-20170525/v5/client"
	"github.com/alibabacloud-go/tea/dara"
)

type smsChannel interface {
	Send(context.Context, SMSConfig, string, string, map[string]string) (string, error)
}

type mailChannel interface {
	Send(context.Context, MailConfig, string, string, string) error
}

type aliyunSMSChannel struct {
	mu         sync.Mutex
	configHash string
	client     *dysms.Client
}

func (c *aliyunSMSChannel) clientFor(config SMSConfig) (*dysms.Client, error) {
	digest := sha256.Sum256([]byte(config.AccessKey + "\x00" + config.AccessSecret))
	hash := hex.EncodeToString(digest[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil && c.configHash == hash {
		return c.client, nil
	}
	endpoint := "dysmsapi.aliyuncs.com"
	connectTimeout, readTimeout := 3000, 5000
	client, err := dysms.NewClient(&openapi.Config{
		AccessKeyId: &config.AccessKey, AccessKeySecret: &config.AccessSecret,
		Endpoint: &endpoint, ConnectTimeout: &connectTimeout, ReadTimeout: &readTimeout,
	})
	if err != nil {
		return nil, err
	}
	c.configHash, c.client = hash, client
	return client, nil
}

func (c *aliyunSMSChannel) Send(ctx context.Context, config SMSConfig, mobile, templateCode string, params map[string]string) (string, error) {
	client, err := c.clientFor(config)
	if err != nil {
		return "", fmt.Errorf("初始化阿里云短信客户端: %w", err)
	}
	payload, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("序列化短信模板参数: %w", err)
	}
	request := (&dysms.SendSmsRequest{}).
		SetPhoneNumbers(mobile).
		SetSignName(config.SignName).
		SetTemplateCode(templateCode).
		SetTemplateParam(string(payload))
	response, err := client.SendSmsWithContext(ctx, request, &dara.RuntimeOptions{})
	if err != nil {
		return "", fmt.Errorf("阿里云短信请求失败: %w", err)
	}
	if response == nil || response.Body == nil {
		return "", errors.New("阿里云短信返回空响应")
	}
	code, message := stringValue(response.Body.Code), stringValue(response.Body.Message)
	if code != "OK" {
		return "", fmt.Errorf("阿里云短信返回 %s: %s", code, message)
	}
	return stringValue(response.Body.BizId), nil
}

type smtpMailChannel struct{}

func (smtpMailChannel) Send(ctx context.Context, config MailConfig, target, subject, htmlBody string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	port := config.Port
	if port == 0 {
		port = 465
	}
	address := net.JoinHostPort(config.Host, fmt.Sprint(port))
	tlsConfig := &tls.Config{ServerName: config.Host, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	var client *smtp.Client
	if port == 465 {
		conn, err := (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
		if err != nil {
			return fmt.Errorf("连接 SMTPS: %w", err)
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		deadline, _ := ctx.Deadline()
		_ = conn.SetDeadline(deadline)
		created, err := smtp.NewClient(conn, config.Host)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("创建 SMTP 客户端: %w", err)
		}
		client = created
	} else {
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return fmt.Errorf("连接 SMTP: %w", err)
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		deadline, _ := ctx.Deadline()
		_ = conn.SetDeadline(deadline)
		created, err := smtp.NewClient(conn, config.Host)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("创建 SMTP 客户端: %w", err)
		}
		client = created
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				_ = client.Close()
				return fmt.Errorf("SMTP STARTTLS: %w", err)
			}
		} else if port == 587 {
			_ = client.Close()
			return errors.New("SMTP 587 服务未提供 STARTTLS")
		}
	}
	defer client.Close()
	if err := client.Auth(smtp.PlainAuth("", config.Username, config.Password, config.Host)); err != nil {
		return fmt.Errorf("SMTP 认证: %w", err)
	}
	if err := client.Mail(config.Username); err != nil {
		return fmt.Errorf("SMTP 发件人: %w", err)
	}
	if err := client.Rcpt(target); err != nil {
		return fmt.Errorf("SMTP 收件人: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP 正文: %w", err)
	}
	fromName := strings.TrimSpace(config.FromName)
	if fromName == "" {
		fromName = "DDYIDC"
	}
	message := fmt.Sprintf("From: %s <%s>\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s", mime.QEncoding.Encode("UTF-8", fromName), config.Username, target, mime.QEncoding.Encode("UTF-8", subject), htmlBody)
	if _, err = writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("写入 SMTP 正文: %w", err)
	}
	if err = writer.Close(); err != nil {
		return fmt.Errorf("提交 SMTP 正文: %w", err)
	}
	if err = client.Quit(); err != nil {
		return fmt.Errorf("结束 SMTP 会话: %w", err)
	}
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
