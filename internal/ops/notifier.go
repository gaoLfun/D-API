package ops

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gaoLfun/dapi/internal/netguard"
)

type Notifier interface {
	Notify(context.Context, Event) error
}

type NotifierFunc func(context.Context, Event) error

func (f NotifierFunc) Notify(ctx context.Context, event Event) error { return f(ctx, event) }

type MultiNotifier []Notifier

func (notifiers MultiNotifier) Notify(ctx context.Context, event Event) error {
	var errs []error
	delivered := 0
	for _, notifier := range notifiers {
		if notifier != nil {
			if err := notifier.Notify(ctx, event); err != nil {
				errs = append(errs, err)
			} else {
				delivered++
			}
		}
	}
	if delivered > 0 {
		if len(errs) > 0 {
			slog.Warn("notification partially delivered", "event", event.Type, "errors", errors.Join(errs...))
		}
		return nil
	}
	return errors.Join(errs...)
}

type CooldownNotifier struct {
	Next     Notifier
	Cooldown time.Duration

	mu   sync.Mutex
	last map[string]sentEvent
	now  func() time.Time
}

type sentEvent struct {
	state string
	at    time.Time
}

func NewCooldownNotifier(next Notifier, cooldown time.Duration) *CooldownNotifier {
	return &CooldownNotifier{Next: next, Cooldown: cooldown, last: make(map[string]sentEvent), now: time.Now}
}

func (n *CooldownNotifier) Notify(ctx context.Context, event Event) error {
	if n == nil || n.Next == nil {
		return nil
	}
	key := fmt.Sprintf("%s:%d", event.Type, event.UpstreamID)
	n.mu.Lock()
	previous, exists := n.last[key]
	now := n.now()
	duplicate := exists && previous.state == event.State && n.Cooldown > 0 && now.Sub(previous.at) < n.Cooldown
	n.mu.Unlock()
	if duplicate {
		return nil
	}
	if err := n.Next.Notify(ctx, event); err != nil {
		return err
	}
	n.mu.Lock()
	n.last[key] = sentEvent{state: event.State, at: now}
	n.mu.Unlock()
	return nil
}

type WebhookConfig struct {
	URL      string
	Provider string
	Headers  map[string]string
	Timeout  time.Duration
}

type WebhookNotifier struct {
	config WebhookConfig
	client *http.Client
}

var webhookTimezone = time.FixedZone("UTC+8", 8*60*60)

func NewWebhookNotifier(config WebhookConfig, client *http.Client) *WebhookNotifier {
	if client == nil {
		client = netguard.NewHTTPClient(config.Timeout)
	}
	client = withoutRedirects(client)
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}
	return &WebhookNotifier{config: config, client: client}
}

func (n *WebhookNotifier) Notify(ctx context.Context, event Event) error {
	body, err := webhookPayload(n.config.Provider, n.config.URL, event)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, n.config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, n.config.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range n.config.Headers {
		if isHopHeader(key) {
			continue
		}
		req.Header.Set(key, value)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook: %w", err)
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	if webhookResponseRejected(responseBody) {
		return errors.New("webhook rejected request")
	}
	return nil
}

func webhookPayload(provider, rawURL string, event Event) ([]byte, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		provider = webhookProviderFromURL(rawURL)
	}
	text := webhookEventText(event)
	switch provider {
	case "dingtalk":
		return json.Marshal(map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": "D-API 通知", "text": webhookEventMarkdown(event)}})
	case "wecom":
		return json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": text}})
	case "feishu":
		return json.Marshal(map[string]any{"msg_type": "text", "content": map[string]string{"text": text}})
	case "slack":
		return json.Marshal(map[string]string{"text": text})
	case "discord":
		return json.Marshal(map[string]string{"content": text})
	default:
		return json.Marshal(event)
	}
}

func webhookProviderFromURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	switch {
	case strings.HasSuffix(host, ".dingtalk.com"):
		return "dingtalk"
	case strings.HasSuffix(host, ".feishu.cn"), strings.HasSuffix(host, ".larksuite.com"):
		return "feishu"
	case host == "qyapi.weixin.qq.com":
		return "wecom"
	case host == "hooks.slack.com":
		return "slack"
	case (host == "discord.com" || host == "discordapp.com") && strings.HasPrefix(parsed.Path, "/api/webhooks/"):
		return "discord"
	default:
		return ""
	}
}

func IsWebhookProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "generic", "dingtalk", "feishu", "wecom", "slack", "discord":
		return true
	default:
		return false
	}
}

func webhookEventText(event Event) string {
	lines := []string{"【D-API】通知", "事件：" + webhookEventLabel(event.Type)}
	if event.UpstreamName != "" {
		lines = append(lines, "上游："+event.UpstreamName)
	}
	if event.State != "" {
		lines = append(lines, "状态："+webhookStateLabel(event.State))
	}
	if event.Previous != "" {
		lines = append(lines, "之前："+webhookStateLabel(event.Previous))
	}
	if event.Message != "" {
		lines = append(lines, "详情："+webhookEventMessage(event))
	}
	if !event.At.IsZero() {
		lines = append(lines, "时间："+event.At.In(webhookTimezone).Format("2006-01-02 15:04:05")+" (UTC+8)")
	}
	return strings.Join(lines, "\n")
}

func webhookEventMarkdown(event Event) string {
	lines := strings.Split(webhookEventText(event), "\n")
	if len(lines) < 2 {
		return strings.Join(lines, "\n")
	}
	for i := 1; i < len(lines); i++ {
		if index := strings.Index(lines[i], "："); index >= 0 {
			lines[i] = "**" + lines[i][:index+len("：")] + "**" + lines[i][index+len("："):]
		}
	}
	return strings.Join(lines, "\n\n")
}

func webhookEventMessage(event Event) string {
	if event.Type == "upstream_health" && event.UpstreamName != "" {
		return "上游状态已从" + webhookStateLabel(event.Previous) + "变更为" + webhookStateLabel(event.State)
	}
	if event.Type == "upstream_balance_protection" && event.UpstreamName != "" {
		return "上游余额保护状态：" + webhookStateLabel(event.State)
	}
	return localizeWebhookMessage(event.Message)
}

func localizeWebhookMessage(message string) string {
	translations := map[string]string{
		"administrator password was changed": "管理员密码已修改",
		"no new administrator login IP":      "未发现新的管理员登录 IP",
	}
	if translated, ok := translations[message]; ok {
		return translated
	}
	return message
}

func webhookEventLabel(eventType string) string {
	switch eventType {
	case "notification_test":
		return "通知渠道测试"
	case "upstream_health":
		return "上游健康状态变更"
	case "low_balance":
		return "上游余额不足"
	case "balance_unavailable":
		return "上游余额不可用"
	case "error_rate":
		return "错误率告警"
	case "latency":
		return "延迟告警"
	case "client_error_rate":
		return "客户端错误率告警"
	case "login_failure":
		return "登录失败告警"
	case "new_login_ip":
		return "新 IP 登录告警"
	case "password_changed":
		return "管理员密码变更"
	case "upstream_balance_protection":
		return "余额保护状态变更"
	default:
		return eventType
	}
}

func webhookStateLabel(state string) string {
	labels := map[string]string{
		"healthy":           "正常",
		"unhealthy":         "异常",
		"success":           "成功",
		"failed":            "失败",
		"firing":            "触发",
		"resolved":          "已恢复",
		"active":            "已启用",
		"paused":            "已暂停",
		"balance_suspended": "已暂停路由",
		"balance_resumed":   "已恢复路由",
	}
	if label, ok := labels[state]; ok {
		return label + "（" + state + "）"
	}
	return state
}

// Several webhook providers return HTTP 2xx for an application-level failure.
// Treat only explicit failure fields as rejection so arbitrary successful
// response bodies remain compatible.
func webhookResponseRejected(body []byte) bool {
	var response map[string]json.RawMessage
	if json.Unmarshal(body, &response) != nil {
		return false
	}
	for _, key := range []string{"success", "ok"} {
		if raw, ok := response[key]; ok {
			var value bool
			if json.Unmarshal(raw, &value) == nil && !value {
				return true
			}
		}
	}
	if raw, ok := response["errcode"]; ok && !webhookSuccessCode(raw, false) {
		return true
	}
	if raw, ok := response["code"]; ok && !webhookSuccessCode(raw, true) {
		return true
	}
	return false
}

func webhookSuccessCode(raw json.RawMessage, allowHTTPStatus bool) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	switch value := value.(type) {
	case float64:
		return value == 0 || (allowHTTPStatus && value == 200)
	case string:
		value = strings.ToLower(strings.TrimSpace(value))
		return value == "0" || (allowHTTPStatus && value == "200") || value == "ok" || value == "success"
	default:
		return true
	}
}

func isHopHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "host", "content-length":
		return true
	default:
		return false
	}
}

type SMTPConfig struct {
	Address     string
	Username    string
	Password    string
	From        string
	To          []string
	ImplicitTLS bool
	Timeout     time.Duration
}

type SMTPNotifier struct{ config SMTPConfig }

func NewSMTPNotifier(config SMTPConfig) *SMTPNotifier {
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}
	return &SMTPNotifier{config: config}
}

func (n *SMTPNotifier) Notify(ctx context.Context, event Event) error {
	requestCtx, cancel := context.WithTimeout(ctx, n.config.Timeout)
	defer cancel()
	host, _, err := net.SplitHostPort(n.config.Address)
	if err != nil || n.config.From == "" || len(n.config.To) == 0 {
		return errors.New("invalid SMTP configuration")
	}
	conn, err := (&netguard.Dialer{Timeout: n.config.Timeout}).DialContext(requestCtx, "tcp", n.config.Address)
	if err != nil {
		return fmt.Errorf("connect SMTP: %w", err)
	}
	if n.config.ImplicitTLS {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(requestCtx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
		conn = tlsConn
	}
	defer conn.Close()
	if deadline, ok := requestCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(n.config.Timeout))
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("start SMTP: %w", err)
	}
	defer client.Close()
	if !n.config.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("start SMTP TLS: %w", err)
			}
		}
	}
	if n.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", n.config.Username, n.config.Password, host)); err != nil {
			return fmt.Errorf("authenticate SMTP: %w", err)
		}
	}
	if err := client.Mail(n.config.From); err != nil {
		return err
	}
	for _, recipient := range n.config.To {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	payload, _ := json.MarshalIndent(event, "", "  ")
	subject := cleanHeader(fmt.Sprintf("[D-API] %s %s", event.UpstreamName, event.State))
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: application/json; charset=UTF-8\r\n\r\n%s\r\n", cleanHeader(n.config.From), cleanHeader(strings.Join(n.config.To, ", ")), subject, payload)
	if _, err := writer.Write([]byte(message)); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func cleanHeader(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}
