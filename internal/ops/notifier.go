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
	"strings"
	"sync"
	"time"
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
	URL     string
	Headers map[string]string
	Timeout time.Duration
}

type WebhookNotifier struct {
	config WebhookConfig
	client *http.Client
}

func NewWebhookNotifier(config WebhookConfig, client *http.Client) *WebhookNotifier {
	if client == nil {
		client = http.DefaultClient
	}
	client = withoutRedirects(client)
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}
	return &WebhookNotifier{config: config, client: client}
}

func (n *WebhookNotifier) Notify(ctx context.Context, event Event) error {
	body, err := json.Marshal(event)
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
		req.Header.Set(key, value)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
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
	host, _, err := net.SplitHostPort(n.config.Address)
	if err != nil || n.config.From == "" || len(n.config.To) == 0 {
		return errors.New("invalid SMTP configuration")
	}
	dialer := &net.Dialer{Timeout: n.config.Timeout}
	var conn net.Conn
	if n.config.ImplicitTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", n.config.Address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", n.config.Address)
	}
	if err != nil {
		return fmt.Errorf("connect SMTP: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
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
